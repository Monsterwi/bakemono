import unittest
from ats_comparison import ats_records, ats_remap, delta, mixed_paths
from acceptance import MIB


class ComparisonConfigTest(unittest.TestCase):
    def test_shared_budget_and_collapse_are_explicit(self):
        records = ats_records(19090)["records"]
        self.assertEqual(records["exec_thread.limit"], 8)
        self.assertEqual(records["exec_thread.autoconfig.enabled"], 0)
        self.assertEqual(records["cache.ram_cache.size"], 10 * MIB)
        self.assertEqual(records["http.cache.open_write_fail_action"], 5)
        self.assertEqual(records["http.cache.max_open_read_retries"], 1000)
        self.assertNotEqual(records["admin.user_id"], "root")

    def test_slice_cache_key_is_set_once_with_range(self):
        native = ats_remap("http://127.0.0.1:8080", "ats-native")
        sliced = ats_remap("http://127.0.0.1:8080", "ats-slice")
        self.assertNotIn("cache_range_requests", native)
        for token in ("--sort-params=true", "--include-headers=Range", "--no-modify-cachekey",
                      "--blockbytes=1048576", "--consider-ident", "--verify-cacheability"):
            self.assertIn(token, sliced)
        self.assertLess(sliced.index("slice.so"), sliced.index("cachekey.so"))
        self.assertEqual(sliced.count("@plugin=cachekey.so"), 1)
        self.assertIn("--prefetch-count=0", ats_remap("http://127.0.0.1:8080", "ats-slice", 0))

    def test_mixed_trace_has_long_tail_and_exceeds_cache(self):
        paths = mixed_paths()
        self.assertEqual(sum("small" in p for p in paths), 256)
        self.assertEqual(sum("large" in p for p in paths), 56)
        self.assertEqual(len(set(paths)), 36)
        self.assertEqual(paths.count("/large-popular-0"), 10)
        self.assertEqual(paths.count("/large-tail-0"), 1)
        self.assertGreater(20 * 100 * MIB, 1024 * MIB)

    def test_origin_deltas_preserve_extra_head_cost(self):
        self.assertEqual(delta({"get": 2, "head": 1, "bytes": 10},
                               {"get": 3, "head": 4, "bytes": 20}), {"get": 1, "head": 3, "bytes": 10})


if __name__ == "__main__":
    unittest.main()
