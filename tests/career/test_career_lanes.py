import tempfile
import unittest
from pathlib import Path
from career_fleet.profile import IdealEmployerProfile
from career_fleet.store import CareerStore
from career_fleet.lanes.lane2_triage import check_dealbreakers, run_lane2_triage
from career_fleet.lanes.lane3_systems import score_technical_wedge, run_lane3_systems


class TestLanes(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.db_path = Path(self.tmpdir.name) / "test_lanes.db"
        self.store = CareerStore(self.db_path)
        self.profile = IdealEmployerProfile()

    def tearDown(self):
        self.tmpdir.cleanup()

    def test_lane2_disqualifies_office_mandate(self):
        comp = {"id": "c1", "headcount": 30}
        postings = [{"raw_text": "We require mandatory in-person presence 5 days a week in our SF office."}]
        dq = check_dealbreakers(comp, postings, self.profile)
        self.assertIsNotNone(dq)
        self.assertTrue(dq["disqualified"])
        self.assertEqual(dq["rule"], "office_mandate")

    def test_lane2_disqualifies_headcount(self):
        comp = {"id": "c2", "headcount": 250}
        postings = [{"raw_text": "Remote engineer role."}]
        dq = check_dealbreakers(comp, postings, self.profile)
        self.assertIsNotNone(dq)
        self.assertEqual(dq["rule"], "headcount_limit")

    def test_lane2_passes_eligible_company(self):
        comp = {"id": "c3", "headcount": 25}
        postings = [{"raw_text": "We are a fully remote distributed team building data systems."}]
        dq = check_dealbreakers(comp, postings, self.profile)
        self.assertIsNone(dq)

    def test_lane3_systems_scoring(self):
        text = "Scaling distributed storage engine and Kafka event-driven workflow engine with PostgreSQL."
        res = score_technical_wedge(text, self.profile)
        self.assertGreaterEqual(res["score"], 0.6)
        self.assertIn(res["verdict"], ("STRONG FIT", "HIGH FIT"))
        self.assertTrue(len(res["quotes"]) > 0)
