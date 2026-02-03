import logging
import csv
import random

from locust import HttpUser, task, between

class CTSLiteUser(HttpUser):
    host = "http://localhost:8080"
    wait_time = between(1, 5)
    
    # Class-level variable to store CSV lines (shared by all users)
    lines = []

    # Class method to load data only once
    @classmethod
    def on_start_class(cls):
        if not cls.lines:  # only load if empty
            file = "../../data/test_data/loadtest_pubchemlite.csv"
            logging.info(f"Reading data from {file}")
            with open(file, "r") as f:
                reader = csv.DictReader(f)
                cls.lines = list(reader)
            logging.info(f"Loaded {len(cls.lines)} lines from {file}")

    def on_start(self):
        # Call class-level loader
        self.on_start_class()

    @task
    def match_queries(self):
        line = random.choice(self.lines)
        query_type = random.choice(["InChIKey", "InChI", "SMILES", "MolecularFormula"])
        query = line[query_type]

        logging.debug(f"Performing query with: {query}")
        
        payload = {"queries": query}

        with self.client.post(
            "/match", 
            json=payload, 
            catch_response=True
        ) as response:
            if response.status_code != 200:
                response.failure(f"Failed with status {response.status_code}: {response.text}")
            else:
                response.success()

