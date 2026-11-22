import unittest

from version_policy import PolicyError, canonical_version, parse_final_tag, resolve_target


class VersionPolicyTest(unittest.TestCase):
    def test_final_tag_grammar(self):
        self.assertEqual(parse_final_tag("v1.2.3"), (1, 2, 3))
        self.assertEqual(parse_final_tag("v0.0.0"), (0, 0, 0))
        self.assertIsNone(parse_final_tag("1.2.3"))
        self.assertIsNone(parse_final_tag("v1.2.3-rc.1"))
        self.assertIsNone(parse_final_tag("v01.2.3"))

    def test_symbolic_targets_use_trusted_baseline(self):
        self.assertEqual(resolve_target("patch", (1, 2, 3)), (1, 2, 4))
        self.assertEqual(resolve_target("minor", (1, 2, 3)), (1, 3, 0))
        self.assertEqual(resolve_target("major", (1, 2, 3)), (2, 0, 0))

    def test_first_release_uses_virtual_baseline(self):
        result = canonical_version("minor", [], [], [])
        self.assertEqual(result["version"], "0.1.0")
        self.assertEqual(result["tag"], "v0.1.0")
        self.assertFalse(result["recovery"])

    def test_highest_reachable_final_tag_is_baseline(self):
        result = canonical_version("patch", ["v1.2.3", "v1.4.0", "v2.0.0-rc.1"], [], ["v1.2.3", "v1.4.0"])
        self.assertEqual(result["version"], "1.4.1")
        self.assertEqual(result["baseline"], "1.4.0")

    def test_exact_target_must_advance_baseline(self):
        with self.assertRaises(PolicyError):
            canonical_version("1.2.3", ["v1.2.3"], [], ["v1.2.3"])
        result = canonical_version("1.2.4", ["v1.2.3"], [], ["v1.2.3"])
        self.assertEqual(result["version"], "1.2.4")

    def test_existing_source_tag_recovers_same_identity(self):
        result = canonical_version("patch", ["v1.2.3"], ["v1.2.3"], ["v1.2.3"])
        self.assertEqual(result["version"], "1.2.3")
        self.assertTrue(result["recovery"])
        with self.assertRaises(PolicyError):
            canonical_version("1.2.4", ["v1.2.3"], ["v1.2.3"], ["v1.2.3"])

    def test_tag_owned_by_another_source_fails(self):
        with self.assertRaises(PolicyError):
            canonical_version("patch", ["v1.2.3"], [], ["v1.2.3", "v1.2.4"])

    def test_multiple_source_tags_fail(self):
        with self.assertRaises(PolicyError):
            canonical_version("patch", ["v1.2.3", "v1.2.4"], ["v1.2.3", "v1.2.4"], ["v1.2.3", "v1.2.4"])


if __name__ == "__main__":
    unittest.main()
