import unittest
from contextlib import ExitStack
import json
import os
from pathlib import Path
import subprocess
import tempfile

from acceptance import CHUNK, Origin, create_fixtures
from hey_benchmark import TEMPLATE, checked_summary


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

    @unittest.skipUnless(os.environ.get("RAZOR_CHECKED_HEY"), "set RAZOR_CHECKED_HEY to exercise the real load client")
    def test_real_load_client_reports_truncated_body_and_counts_complete_bytes(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as cleanup:
            root = Path(directory)
            fixtures = create_fixtures(root / "fixtures", CHUNK + 17)
            origin = Origin(fixtures, root / "origin.json")
            cleanup.callback(origin.close)
            origin.rules["/small-truncated"] = {"truncate": True}
            def request(path):
                return json.loads(subprocess.check_output([os.environ["RAZOR_CHECKED_HEY"], "-n", "1", "-c", "1",
                    "-t", "5", "-o", TEMPLATE, origin.url + path], text=True, timeout=10))
            truncated = request("/small-truncated")
            self.assertTrue(truncated["errors"], "load client silently accepted a truncated body")
            complete = checked_summary(request("/small-complete"), 256 << 10)
            self.assertEqual(complete["requests"], 1)


if __name__ == "__main__":
    unittest.main()
