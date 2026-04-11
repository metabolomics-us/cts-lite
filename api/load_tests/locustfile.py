import logging
import csv
import random

from locust import HttpUser, task, between, events

HOSTS = {
    "local": "http://localhost:8080",
    "remote": "https://cts-lite.metabolomics.us",
}


@events.init_command_line_parser.add_listener
def _(parser):
    parser.add_argument(
        "--env",
        choices=["local", "remote"],
        default="local",
        help="Target environment (default: local)",
    )


@events.init.add_listener
def on_locust_init(environment, **kwargs):
    env = environment.parsed_options.env if environment.parsed_options else "local"
    CTSLiteUser.host = HOSTS[env]
    logging.info(f"Running against {env} host: {CTSLiteUser.host}")


class CTSLiteUser(HttpUser):
    host = HOSTS["local"]
    wait_time = between(1, 5)

    # Class-level variable to store CSV lines (shared by all users)
    lines = []

    @classmethod
    def on_start_class(cls):
        if not cls.lines:
            file = "../../dataset/test_datasets/loadtest_data.csv"
            logging.info(f"Reading data from {file}")
            with open(file, "r") as f:
                reader = csv.DictReader(f)
                cls.lines = list(reader)
            logging.info(f"Loaded {len(cls.lines)} lines from {file}")

    def on_start(self):
        self.on_start_class()

    @task
    def match_queries(self):
        num_lines = random.randint(1, 40)
        rows = random.sample(self.lines, num_lines)

        queries = []
        for row in rows:
            query_type = random.choice(["InChIKey", "SMILES", "InChI", "MolecularFormula"])
            queries.append(row[query_type])

        logging.debug(f"Performing query with: {queries}")

        payload = {"queries": " ".join(queries)}

        with self.client.post(
            "/match",
            json=payload,
            catch_response=True,
        ) as response:
            if response.status_code != 200:
                response.failure(f"Failed with status {response.status_code}: {response.text}")
            else:
                response.success()
