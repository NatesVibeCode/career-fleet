import tempfile
import unittest
from pathlib import Path
from career_fleet.profile import IdealEmployerProfile, Dealbreakers


class TestProfile(unittest.TestCase):
    def test_default_profile(self):
        prof = IdealEmployerProfile()
        self.assertIsNone(prof.dealbreakers.max_headcount)
        self.assertEqual(prof.dealbreakers.policy, "any")
        self.assertFalse(prof.dealbreakers.reject_thin_wrappers)
        self.assertEqual(prof.wedge_capabilities, [])

    def test_save_and_load(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            p_path = Path(tmpdir) / "test_prof.json"
            prof = IdealEmployerProfile(profile_name="Test Operator")
            prof.save(p_path)
            self.assertTrue(p_path.exists())

            loaded = IdealEmployerProfile.load(p_path)
            self.assertEqual(loaded.profile_name, "Test Operator")
            self.assertIsNone(loaded.dealbreakers.max_headcount)
