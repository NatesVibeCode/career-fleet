import sqlite3
import tempfile
import unittest
from pathlib import Path

from career_fleet.profile import IdealEmployerProfile
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

    def test_profile_revisions_are_stored_and_active_profile_round_trips(self):
        first = IdealEmployerProfile(profile_name="First", dealbreakers={"policy": "any"})
        second = IdealEmployerProfile(profile_name="Second", dealbreakers={"policy": "remote_only"})

        first_revision = self.store.save_profile(first)
        self.assertEqual(self.store.active_profile_revision_id(), first_revision)
        self.assertEqual(self.store.load_profile().model_dump(), first.model_dump())

        second_revision = self.store.save_profile(second)
        self.assertNotEqual(second_revision, first_revision)
        self.assertEqual(self.store.active_profile_revision_id(), second_revision)
        self.assertEqual(self.store.load_profile().model_dump(), second.model_dump())
        with self.store.connect() as con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM profile_revisions").fetchone()[0], 2)

    def test_existing_database_gets_new_source_metadata_columns(self):
        legacy_path = Path(self.tmpdir.name) / "legacy.db"
        legacy = sqlite3.connect(legacy_path)
        legacy.executescript(
            """
            CREATE TABLE companies (
                id TEXT PRIMARY KEY, name TEXT NOT NULL, domain TEXT, stage TEXT,
                headcount INT, hq_location TEXT, ats_provider TEXT, ats_token TEXT,
                website_url TEXT, status TEXT DEFAULT 'discovered',
                disqualification_reason TEXT,
                created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            );
            CREATE TABLE job_postings (
                id TEXT PRIMARY KEY, company_id TEXT NOT NULL, title TEXT NOT NULL,
                location TEXT, is_remote BOOLEAN DEFAULT 0, job_url TEXT,
                raw_text TEXT NOT NULL, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
            );
            """
        )
        legacy.close()

        CareerStore(legacy_path)
        # NOTE: sqlite3 connections do not close on `with` exit (the block
        # only commits); an explicit close is required so Windows tmp
        # cleanup does not hit a locked file.
        migrated = sqlite3.connect(legacy_path)
        try:
            company_columns = {row[1] for row in migrated.execute("PRAGMA table_info(companies)")}
            posting_columns = {row[1] for row in migrated.execute("PRAGMA table_info(job_postings)")}
        finally:
            migrated.close()

        self.assertIn("timezone", company_columns)
        self.assertIn("timezone", posting_columns)
        self.assertIn("source_type", posting_columns)

    def test_evaluations_require_a_real_iep_revision(self):
        self.store.upsert_company("acme", "Acme")
        with self.assertRaisesRegex(ValueError, "profile revision does not exist"):
            self.store.record_evaluation(
                "ev-missing", "acme", "lane2_triage", "triaged", 1.0,
                "SURVIVOR", "test", profile_revision_id="missing-revision",
            )

    def test_new_database_has_profile_foreign_keys(self):
        with self.store.connect() as connection:
            active_fks = connection.execute("PRAGMA foreign_key_list(active_profiles)").fetchall()
            eval_fks = connection.execute("PRAGMA foreign_key_list(evaluations)").fetchall()
        self.assertTrue(any(row[2] == "profile_revisions" and row[3] == "revision_id" for row in active_fks))
        self.assertTrue(any(row[2] == "profile_revisions" and row[3] == "profile_revision_id" for row in eval_fks))

    def test_company_domains_are_normalized_before_matching(self):
        self.store.upsert_company("acme", "Acme", domain="https://www.Example.com.:443/jobs")
        self.assertEqual(self.store.get_company_by_domain("example.com.")["id"], "acme")
        self.assertEqual(self.store.list_companies()[0]["domain"], "example.com")

    def test_community_snapshot_keeps_unlinked_leads_and_linked_postings(self):
        self.store.upsert_company("acme.example", "Acme", domain="acme.example")
        profile_revision = self.store.save_profile(IdealEmployerProfile(profile_name="Community Profile"))
        result = self.store.replace_community_source_snapshot(
            "reddit",
            "reddit:career-query",
            [
                {
                    "id": "community-reddit-1",
                    "title": "Hiring a Staff Engineer",
                    "source_uri": "https://www.reddit.com/r/startups/comments/abc/hiring/",
                    "raw_text": "Hiring a Staff Engineer. Fully remote.",
                    "relevance_score": 0.8,
                    "signal_types": ["hiring", "role", "workplace"],
                    "metadata": {"is_remote": True},
                    "company_id": "acme.example",
                },
                {
                    "id": "community-reddit-2",
                    "title": "Remote engineering discussion",
                    "source_uri": "https://www.reddit.com/r/experienceddevs/comments/def/discussion/",
                    "raw_text": "What makes a good remote engineering team?",
                    "relevance_score": 0.4,
                    "signal_types": ["role", "workplace", "leadership"],
                    "metadata": {},
                },
            ],
            profile_revision_id=profile_revision,
        )

        self.assertEqual(result, {"signals_added": 2, "linked_postings_added": 1, "linked_companies": 1})
        self.assertEqual(len(self.store.list_community_signals(linked=False)), 1)
        dossier = self.store.get_company_dossier("acme.example")
        self.assertEqual(len(dossier["jobs"]), 1)
        self.assertEqual(dossier["jobs"][0]["source_type"], "reddit")
        self.assertEqual(len(dossier["community_signals"]), 1)
        self.assertEqual(dossier["community_signals"][0]["profile_revision_id"], profile_revision)
