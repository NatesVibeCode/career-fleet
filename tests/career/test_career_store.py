import tempfile
import unittest
from pathlib import Path
from career_fleet.store import CareerStore


class TestStore(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.db_path = Path(self.tmpdir.name) / "test_store.db"
        self.store = CareerStore(self.db_path)

    def tearDown(self):
        self.tmpdir.cleanup()

    def test_upsert_and_dossier(self):
        self.store.upsert_company(
            company_id="acme",
            name="Acme Systems",
            domain="acme.internal",
            headcount=45,
            timezone="America/New_York",
            status="discovered"
        )
        self.store.add_job_posting(
            job_id="job-1",
            company_id="acme",
            title="Systems Engineer",
            raw_text="Building Kafka and PostgreSQL distributed state engines.",
            location="New York, NY",
            timezone="America/New_York",
            is_remote=True
        )
        self.store.record_evaluation(
            eval_id="ev-1",
            company_id="acme",
            lane="lane3_systems",
            status="qualified",
            score=0.9,
            verdict="HIGH FIT",
            rationale="Strong state engine",
            quotes=["Kafka and PostgreSQL distributed state engines"]
        )

        dossier = self.store.get_company_dossier("acme")
        self.assertIsNotNone(dossier)
        self.assertEqual(dossier["name"], "Acme Systems")
        self.assertEqual(len(dossier["jobs"]), 1)
        self.assertEqual(dossier["jobs"][0]["raw_text"], "Building Kafka and PostgreSQL distributed state engines.")
        self.assertEqual(dossier["jobs"][0]["timezone"], "America/New_York")
        self.assertEqual(len(dossier["evaluations"]), 1)
        self.assertEqual(dossier["evaluations"][0]["score"], 0.9)

    def test_in_memory_store_persists_between_operations(self):
        store = CareerStore(":memory:")
        try:
            store.upsert_company(company_id="memory", name="Memory Co")
            self.assertEqual(store.list_companies()[0]["id"], "memory")
        finally:
            store.close()
