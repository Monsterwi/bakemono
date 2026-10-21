"""New public path uses real TCP origins; optional real Razor binary in CI."""
from contextlib import ExitStack
from pathlib import Path
from types import SimpleNamespace
import http.client
import os
import tempfile
import threading
import unittest

from acceptance import Client, Origin, create_fixtures, MIB


class PublicToolsTest(unittest.TestCase):
    def test_block_fault_is_scoped_and_source_address_is_real(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as cleanup:
            root = Path(directory)
            fixtures = create_fixtures(root / "fixtures", 3 * MIB)
            origin = Origin(fixtures, root / "origin.json")
            cleanup.callback(origin.close)
            origin.rules["/large-fault"] = {"block_start": MIB, "force_status": 200}
            client = Client(fixtures, root)
            cleanup.callback(client.raw.close)
            product = SimpleNamespace(kind="origin", port=origin.server.server_port)
            for start, status in ((0, 206), (MIB, 200)):
                row = client.request(product, "/large-fault", headers={"Range": f"bytes={start}-{start + MIB - 1}"},
                                     status=status, source_ip="127.0.0.42")
                self.assertFalse(row["error"], row)
            self.assertEqual({r["remote_ip"] for r in origin.records}, {"127.0.0.42"})
            self.assertEqual([r["status"] for r in origin.records], [206, 200])

    def test_range_drag_changes_interval_between_requests(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as cleanup:
            root = Path(directory)
            fixtures = create_fixtures(root / "fixtures", MIB)
            origin = Origin(fixtures, root / "origin.json")
            cleanup.callback(origin.close)
            (root / "process.log").touch()
            client = Client(fixtures, root)
            cleanup.callback(client.raw.close)
            product = SimpleNamespace(kind="origin", port=origin.server.server_port, dir=root,
                resource=lambda: {"time":0,"cpu_seconds":0,"rss_bytes":0,"fds":0,"read_bytes":0,"write_bytes":0})
            client.batch(product, origin, "drag", "/large-drag", 1, count=3, ranges=[(0,15),(128,143),(256,271)])
            self.assertEqual({row["range"] for row in origin.records}, {"bytes=0-15", "bytes=128-143", "bytes=256-271"})

    @unittest.skipUnless(os.environ.get("RAZOR_ACCEPTANCE_BINARY"), "set RAZOR_ACCEPTANCE_BINARY for real binary smoke")
    def test_real_binary_short_soak_has_all_phases_and_hard_drain(self):
        from public_acceptance import mixed_soak, DRAIN_FIELDS
        import json
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fixtures = create_fixtures(root / "fixtures", 8 * MIB)
            args = SimpleNamespace(binary=Path(os.environ["RAZOR_ACCEPTANCE_BINARY"]), cache_root=None,
                public_static=True, ram_algorithm="s3-fifo", ram_bytes=10 * MIB, block_bytes=MIB,
                soak_seconds=.05, allow_short_soak=True)
            result = mixed_soak(args, fixtures, root)
            self.assertFalse(result["qualifies_two_hour_soak"])
            phases = [json.loads(line)["phase"] for line in (root / "soak-stages.jsonl").read_text().splitlines()]
            self.assertEqual(len(set(phases)), 7)
            for line in (root / "soak-stages.jsonl").read_text().splitlines():
                stage = json.loads(line)
                self.assertEqual(stage["batch"]["cache_metrics"]["delta"]["Requests"], stage["batch"]["requests"])
            self.assertGreater(result["requests"], 7)
            self.assertTrue(all(result["final"]["stats"][field] == 0 for field in DRAIN_FIELDS))
            self.assertTrue((root / "origin-soak.jsonl").stat().st_size)

    @unittest.skipUnless(os.environ.get("RAZOR_ACCEPTANCE_BINARY"), "set RAZOR_ACCEPTANCE_BINARY for real binary smoke")
    def test_real_binary_public_contracts_both_policies(self):
        from public_acceptance import public_functional
        for algorithm in ("otter", "s3-fifo"):
            with self.subTest(algorithm=algorithm), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                fixtures = create_fixtures(root / "fixtures", 4 * MIB)
                args = SimpleNamespace(binary=Path(os.environ["RAZOR_ACCEPTANCE_BINARY"]), cache_root=None,
                    public_static=True, ram_algorithm=algorithm, ram_bytes=10 * MIB, block_bytes=MIB)
                public_functional(args, fixtures, root, smoke=True)
