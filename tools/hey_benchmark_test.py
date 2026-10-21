import unittest

from hey_benchmark import checked_summary


class HeySummaryTest(unittest.TestCase):
    def test_rejects_error_status_and_wrong_declared_length(self):
        raw = {"seconds": 2, "requests": 2, "bytes": 524288, "rps": 1,
               "status": {"200": 2}, "errors": None,
               "latency": [{"Percentage": 99, "Latency": .01}]}
        result = checked_summary(raw, 262144)
        self.assertEqual(result["mib_per_second"], .25)
        self.assertEqual(result["completion_ms"]["p99"], 10)
        single = checked_summary({**raw, "latency": [{"Percentage": 0, "Latency": 0}]}, 262144)
        self.assertEqual(single["completion_ms"], {})
        for change in ({"status": {"200": 1, "502": 1}}, {"errors": {"EOF": 1}},
                       {"bytes": 262144}, {"requests": 0}, {"seconds": 0}):
            with self.subTest(change=change), self.assertRaises(AssertionError):
                checked_summary({**raw, **change}, 262144)


if __name__ == "__main__":
    unittest.main()
