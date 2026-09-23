// Command deploy is the daemonless build/push/deploy half of the cts-lite
// Woodpecker pipeline. It replaces the build_and_push_image and
// deploy_to_ecs jobs of the removed GitHub Actions workflow (cicd.yml).
//
// The Woodpecker exec hosts have no docker and no aws CLI, so the three
// things those jobs needed a daemon or the CLI for are done here with the
// AWS SDK and go-containerregistry:
//
//	fetch-dataset  s3 GetObject + gunzip of the dataset csv
//	build-push     base image + prebuilt layers -> ECR, one tag
//	tag            point another tag at an already-pushed digest
//	ecs-deploy     force a new deployment and wait for steady state
//
// Credentials come only from the environment (AWS_ACCESS_KEY_ID /
// AWS_SECRET_ACCESS_KEY, injected by Woodpecker from repo secrets). The ECR
// password lives only in memory: it is never written to a docker config,
// passed in argv, or printed.
//
// It is its own Go module so the AWS SDK and go-containerregistry do not
// become dependencies of the service, and so `go build ./...` and the
// coverage gate at the repository root do not see it.
package main

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

const region = "us-west-2"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "fetch-dataset":
		err = fetchDataset(ctx, os.Args[2:])
	case "build-push":
		err = buildPush(ctx, os.Args[2:])
	case "tag":
		err = tag(ctx, os.Args[2:])
	case "ecs-deploy":
		err = ecsDeploy(ctx, os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: deploy fetch-dataset|build-push|tag|ecs-deploy [flags]")
	os.Exit(2)
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func awsConfig(ctx context.Context) (aws.Config, error) {
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
		return aws.Config{}, errors.New("AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY are not set (Woodpecker secrets are only injected on push and manual events)")
	}
	return config.LoadDefaultConfig(ctx, config.WithRegion(region))
}

// fetchDataset streams s3://bucket/key through gunzip into -out, which is
// what `aws s3 cp` + `gzip -d` did, without the 1.4 GB .gz on disk.
func fetchDataset(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch-dataset", flag.ExitOnError)
	bucket := fs.String("bucket", "cts-lite-datasets", "")
	key := fs.String("key", "cts-lite_latest.csv.gz", "")
	out := fs.String("out", "", "destination csv")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("-out is required")
	}
	cfg, err := awsConfig(ctx)
	if err != nil {
		return err
	}
	obj, err := s3.NewFromConfig(cfg).GetObject(ctx, &s3.GetObjectInput{Bucket: bucket, Key: key})
	if err != nil {
		return err
	}
	defer obj.Body.Close()
	fmt.Printf("s3://%s/%s: %d bytes, last modified %s, etag %s\n", *bucket, *key,
		aws.ToInt64(obj.ContentLength), aws.ToTime(obj.LastModified).UTC().Format(time.RFC3339), aws.ToString(obj.ETag))
	zr, err := gzip.NewReader(obj.Body)
	if err != nil {
		return err
	}
	tmp := *out + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, zr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	fmt.Printf("wrote %s: %d bytes uncompressed\n", *out, n)
	return os.Rename(tmp, *out)
}

func ecrAuth(ctx context.Context) (authn.Authenticator, error) {
	cfg, err := awsConfig(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return nil, err
	}
	if len(res.AuthorizationData) == 0 {
		return nil, errors.New("ecr returned no authorization data")
	}
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(res.AuthorizationData[0].AuthorizationToken))
	if err != nil {
		return nil, err
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, errors.New("malformed ecr authorization token")
	}
	return &authn.Basic{Username: user, Password: pass}, nil
}

// registryAuth is ECR auth for ECR, anonymous otherwise -- the latter only so
// the image assembly can be exercised against a local throwaway registry
// (`crane registry serve`) without AWS.
func registryAuth(ctx context.Context, repo string) (authn.Authenticator, error) {
	host, _, _ := strings.Cut(repo, "/")
	if !strings.HasSuffix(host, ".amazonaws.com") {
		return authn.Anonymous, nil
	}
	return ecrAuth(ctx)
}

// The runtime contract of the production image, as the Dockerfile set it:
// WORKDIR /app, CMD ["./ctslite"], EXPOSE 8080, no USER, no ENTRYPOINT.
func applyRuntimeConfig(c v1.Config) v1.Config {
	c.WorkingDir = "/app"
	c.Cmd = []string{"./ctslite"}
	c.Entrypoint = nil
	c.User = ""
	c.ExposedPorts = map[string]struct{}{"8080/tcp": {}}
	c.ArgsEscaped = true
	return c
}

func envValue(env []string, k string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, k+"="); ok {
			return v
		}
	}
	return ""
}

// compareRuntime reports how the new image's config differs from the one
// currently serving, and fails on anything that changes how the container
// starts. GOLANG_VERSION is expected to move with the base image.
func compareRuntime(cur, next v1.Config) error {
	var hard []string
	check := func(field string, a, b any) {
		as, bs := fmt.Sprint(a), fmt.Sprint(b)
		mark := "same"
		if as != bs {
			mark = "DIFFERS"
			hard = append(hard, field)
		}
		fmt.Printf("  %-13s %-7s current=%s new=%s\n", field, mark, as, bs)
	}
	check("WorkingDir", cur.WorkingDir, next.WorkingDir)
	check("Cmd", cur.Cmd, next.Cmd)
	check("Entrypoint", cur.Entrypoint, next.Entrypoint)
	check("User", cur.User, next.User)
	check("ExposedPorts", sortedKeys(cur.ExposedPorts), sortedKeys(next.ExposedPorts))
	check("PATH", envValue(cur.Env, "PATH"), envValue(next.Env, "PATH"))
	check("GOPATH", envValue(cur.Env, "GOPATH"), envValue(next.Env, "GOPATH"))
	check("GOTOOLCHAIN", envValue(cur.Env, "GOTOOLCHAIN"), envValue(next.Env, "GOTOOLCHAIN"))
	fmt.Printf("  %-13s info    current=%s new=%s\n", "GOLANG_VERSION", envValue(cur.Env, "GOLANG_VERSION"), envValue(next.Env, "GOLANG_VERSION"))
	curKeys, nextKeys := envKeys(cur.Env), envKeys(next.Env)
	check("Env keys", curKeys, nextKeys)
	if len(hard) > 0 {
		return fmt.Errorf("new image changes the runtime contract: %s", strings.Join(hard, ", "))
	}
	return nil
}

func sortedKeys(m map[string]struct{}) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func envKeys(env []string) []string {
	var ks []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func buildPush(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build-push", flag.ExitOnError)
	base := fs.String("base", "", "base image, pinned by digest")
	expectGo := fs.String("expect-go", "", "GOLANG_VERSION the base must carry (the binary was built with it)")
	repo := fs.String("repo", "", "target repository, e.g. <acct>.dkr.ecr.us-west-2.amazonaws.com/cts-lite")
	tagName := fs.String("tag", "", "the one tag to push")
	compareTag := fs.String("compare", "latest", "tag of the image currently serving, to compare configs against")
	digestOut := fs.String("digest-out", "", "write the pushed manifest digest here")
	var layers multi
	fs.Var(&layers, "layer", "gzipped layer tarball, in order (repeatable)")
	_ = fs.Parse(args)
	if *base == "" || *repo == "" || *tagName == "" || len(layers) == 0 {
		return errors.New("-base, -repo, -tag and at least one -layer are required")
	}
	if !strings.Contains(*base, "@sha256:") {
		return fmt.Errorf("base %q is not pinned by digest", *base)
	}
	if *tagName == "latest" {
		return errors.New("never push :latest directly; push the sha tag, then `tag` it")
	}

	baseRef, err := name.ParseReference(*base)
	if err != nil {
		return err
	}
	platform := v1.Platform{OS: "linux", Architecture: "amd64"}
	baseImg, err := remote.Image(baseRef, remote.WithContext(ctx), remote.WithPlatform(platform))
	if err != nil {
		return fmt.Errorf("pull base: %w", err)
	}
	baseCfg, err := baseImg.ConfigFile()
	if err != nil {
		return err
	}
	if got := envValue(baseCfg.Config.Env, "GOLANG_VERSION"); *expectGo != "" && got != *expectGo {
		return fmt.Errorf("base carries GOLANG_VERSION=%s, the binary was built with %s", got, *expectGo)
	}

	img := baseImg
	for _, path := range layers {
		l, err := tarball.LayerFromFile(path)
		if err != nil {
			return fmt.Errorf("layer %s: %w", path, err)
		}
		img, err = mutate.Append(img, mutate.Addendum{
			Layer: l,
			History: v1.History{
				Created:   v1.Time{Time: time.Now().UTC()},
				CreatedBy: "ops/ci/deploy build-push " + path,
				Comment:   "woodpecker " + os.Getenv("CI_COMMIT_SHA"),
			},
		})
		if err != nil {
			return err
		}
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return err
	}
	cf = cf.DeepCopy()
	cf.Config = applyRuntimeConfig(cf.Config)
	cf.Created = v1.Time{Time: time.Now().UTC()}
	img, err = mutate.ConfigFile(img, cf)
	if err != nil {
		return err
	}

	auth, err := registryAuth(ctx, *repo)
	if err != nil {
		return fmt.Errorf("ecr auth: %w", err)
	}
	opts := []remote.Option{remote.WithContext(ctx), remote.WithAuth(auth)}

	if *compareTag != "" {
		cur, err := name.ParseReference(*repo + ":" + *compareTag)
		if err != nil {
			return err
		}
		curImg, err := remote.Image(cur, append(opts, remote.WithPlatform(platform))...)
		if err != nil {
			return fmt.Errorf("read %s to compare: %w", cur, err)
		}
		curCfg, err := curImg.ConfigFile()
		if err != nil {
			return err
		}
		fmt.Printf("runtime config vs %s:\n", cur)
		if err := compareRuntime(curCfg.Config, cf.Config); err != nil {
			return err
		}
	}

	dst, err := name.NewTag(*repo + ":" + *tagName)
	if err != nil {
		return err
	}
	start := time.Now()
	if err := remote.Write(dst, img, opts...); err != nil {
		return fmt.Errorf("push %s: %w", dst, err)
	}
	d, err := img.Digest()
	if err != nil {
		return err
	}
	fmt.Printf("pushed %s@%s in %s\n", dst, d, time.Since(start).Round(time.Second))
	if *digestOut != "" {
		return os.WriteFile(*digestOut, []byte(d.String()+"\n"), 0o644)
	}
	return nil
}

func tag(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tag", flag.ExitOnError)
	repo := fs.String("repo", "", "")
	digest := fs.String("digest", "", "sha256:... of an image already in -repo")
	to := fs.String("to", "", "tag to point at it")
	_ = fs.Parse(args)
	if *repo == "" || *digest == "" || *to == "" {
		return errors.New("-repo, -digest and -to are required")
	}
	auth, err := registryAuth(ctx, *repo)
	if err != nil {
		return err
	}
	src, err := name.NewDigest(*repo + "@" + strings.TrimSpace(*digest))
	if err != nil {
		return err
	}
	opts := []remote.Option{remote.WithContext(ctx), remote.WithAuth(auth)}
	desc, err := remote.Get(src, opts...)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", src, err)
	}
	dst, err := name.NewTag(*repo + ":" + *to)
	if err != nil {
		return err
	}
	if err := remote.Tag(dst, desc, opts...); err != nil {
		return err
	}
	fmt.Printf("%s -> %s\n", dst, desc.Digest)
	return nil
}

// ecsDeploy is `aws ecs update-service --force-new-deployment` followed by
// a wait that, unlike `aws ecs wait services-stable`, fails as soon as the
// deployment circuit breaker marks the rollout FAILED, and then checks the
// running task really is on the image that was just pushed.
func ecsDeploy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ecs-deploy", flag.ExitOnError)
	cluster := fs.String("cluster", "", "")
	service := fs.String("service", "", "")
	container := fs.String("container", "cts-lite", "container whose image digest is checked")
	wantDigest := fs.String("expect-digest", "", "image digest the running task must report")
	timeout := fs.Duration("timeout", 20*time.Minute, "")
	_ = fs.Parse(args)
	if *cluster == "" || *service == "" {
		return errors.New("-cluster and -service are required")
	}
	cfg, err := awsConfig(ctx)
	if err != nil {
		return err
	}
	c := ecs.NewFromConfig(cfg)
	up, err := c.UpdateService(ctx, &ecs.UpdateServiceInput{
		Cluster: cluster, Service: service, ForceNewDeployment: true,
	})
	if err != nil {
		return err
	}
	var depID string
	for _, d := range up.Service.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" {
			depID = aws.ToString(d.Id)
		}
	}
	if depID == "" {
		return errors.New("update-service returned no PRIMARY deployment")
	}
	fmt.Printf("forced new deployment %s of %s/%s; waiting up to %s\n", depID, *cluster, *service, *timeout)

	deadline := time.Now().Add(*timeout)
	last := ""
	for {
		ds, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: cluster, Services: []string{*service}})
		if err != nil {
			return err
		}
		if len(ds.Services) != 1 {
			return fmt.Errorf("describe-services returned %d services", len(ds.Services))
		}
		s := ds.Services[0]
		var mine *ecstypes.Deployment
		for i := range s.Deployments {
			if aws.ToString(s.Deployments[i].Id) == depID {
				mine = &s.Deployments[i]
			}
		}
		if mine == nil {
			return fmt.Errorf("deployment %s disappeared from the service (rolled back?)", depID)
		}
		state := fmt.Sprintf("rollout=%s running=%d/%d pending=%d deployments=%d",
			mine.RolloutState, mine.RunningCount, mine.DesiredCount, mine.PendingCount, len(s.Deployments))
		if state != last {
			fmt.Printf("%s %s\n", time.Now().UTC().Format(time.TimeOnly), state)
			last = state
		}
		switch mine.RolloutState {
		case ecstypes.DeploymentRolloutStateFailed:
			for _, e := range s.Events[:min(5, len(s.Events))] {
				fmt.Printf("  event %s %s\n", aws.ToTime(e.CreatedAt).UTC().Format(time.RFC3339), aws.ToString(e.Message))
			}
			return fmt.Errorf("deployment FAILED: %s", aws.ToString(mine.RolloutStateReason))
		case ecstypes.DeploymentRolloutStateCompleted:
			if len(s.Deployments) == 1 && mine.RunningCount == mine.DesiredCount {
				fmt.Printf("steady: %s\n", aws.ToString(mine.RolloutStateReason))
				return verifyRunning(ctx, c, *cluster, *service, *container, strings.TrimSpace(*wantDigest))
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not stable after %s (%s); ECS keeps the old task until the new one is healthy, and the circuit breaker may still roll back", *timeout, state)
		}
		time.Sleep(15 * time.Second)
	}
}

func verifyRunning(ctx context.Context, c *ecs.Client, cluster, service, container, want string) error {
	lt, err := c.ListTasks(ctx, &ecs.ListTasksInput{Cluster: &cluster, ServiceName: &service, DesiredStatus: ecstypes.DesiredStatusRunning})
	if err != nil {
		return err
	}
	if len(lt.TaskArns) == 0 {
		return errors.New("service reports steady state but lists no running task")
	}
	dt, err := c.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: &cluster, Tasks: lt.TaskArns})
	if err != nil {
		return err
	}
	bad := 0
	for _, t := range dt.Tasks {
		for _, ct := range t.Containers {
			if aws.ToString(ct.Name) != container {
				continue
			}
			got := aws.ToString(ct.ImageDigest)
			ok := want == "" || got == want
			fmt.Printf("task %s %s health=%s image=%s digest=%s match=%v\n",
				aws.ToString(t.TaskArn), aws.ToString(t.LastStatus), t.HealthStatus, aws.ToString(ct.Image), got, ok)
			if !ok {
				bad++
			}
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d running task(s) are not on %s", bad, want)
	}
	return nil
}
