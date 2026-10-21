import os
from contextlib import ExitStack
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import acceptance


class StartupCleanupTest(unittest.TestCase):
    def product(self, directory, command):
        product = acceptance.Product.__new__(acceptance.Product)
        product.dir = Path(directory)
        product.command = command
        product.env = os.environ.copy()
        product.kind = "test-child"
        product.port = acceptance.free_port()
        product.proc = None
        def cleanup():
            if product.proc is not None:
                if product.proc.poll() is None:
                    product.proc.kill()
                product.proc.wait()
            if hasattr(product, "log"):
                product.log.close()
        self.addCleanup(cleanup)
        return product

    def test_readiness_timeout_reaps_real_child_and_closes_log(self):
        with tempfile.TemporaryDirectory() as directory:
            product = self.product(directory, [sys.executable, "-c", "import time; time.sleep(60)"])
            real_wait = acceptance.wait_for
            def short_wait(check, description, timeout):
                return real_wait(check, description, .1)
            with patch.object(acceptance, "wait_for", short_wait):
                with self.assertRaisesRegex(AssertionError, "timeout: test-child listening"):
                    product.start()
            self.assertIsNotNone(product.proc.returncode, "startup failure did not reap child")
            self.assertTrue(product.log.closed, "startup failure leaked process log")

    def test_popen_failure_closes_log_and_keeps_original_error(self):
        with tempfile.TemporaryDirectory() as directory:
            product = self.product(directory, [str(Path(directory) / "missing-binary")])
            with self.assertRaises(FileNotFoundError):
                product.start()
            self.assertTrue(product.log.closed, "Popen failure leaked process log")

    def test_constructor_failure_closes_real_origins_and_client(self):
        for run in (acceptance.functional, acceptance.benchmark):
            with self.subTest(run=run.__name__), tempfile.TemporaryDirectory() as directory:
                origins, clients = [], []
                real_origin, real_client = acceptance.Origin, acceptance.Client
                def origin(*args, **kwargs):
                    instance = real_origin(*args, **kwargs)
                    origins.append(instance)
                    self.addCleanup(lambda server=instance.server: (server.shutdown(), server.server_close()))
                    return instance
                def client(*args, **kwargs):
                    instance = real_client(*args, **kwargs)
                    clients.append(instance)
                    self.addCleanup(instance.raw.close)
                    return instance
                args = SimpleNamespace(binary=Path(directory) / "missing-binary", products=["razor"])
                with patch.object(acceptance, "Origin", origin), patch.object(acceptance, "Client", client):
                    with self.assertRaises(FileNotFoundError):
                        run(args, {}, Path(directory))
                self.assertTrue(all(not instance.thread.is_alive() for instance in origins))
                self.assertTrue(all(instance.server.fileno() == -1 for instance in origins))
                self.assertTrue(all(instance.raw.closed for instance in clients))


class DeviceSamplingTest(unittest.TestCase):
    def test_diskstats_uses_sectors_not_completed_operations(self):
        text = "8 32 sdc 100 2 1234 7 200 3 5678 9 0 456 789\n8 33 sdc1 1 0 1 0 2 0 2 0 0 3 4\n"
        self.assertEqual(acceptance.parse_device_io(os.makedev(8,32), text),
            {"major":8,"minor":32,"name":"sdc","read_bytes":631808,"write_bytes":2907136,"io_ms":456})
        self.assertIsNone(acceptance.parse_device_io(os.makedev(0,1), text))


class WorkloadTest(unittest.TestCase):
    def test_fixtures_keep_exact_size_tail_and_independent_versions(self):
        with tempfile.TemporaryDirectory() as directory:
            fixtures = acceptance.create_fixtures(Path(directory) / "fixtures", acceptance.CHUNK + 17)
            for version in (1, 2):
                self.assertEqual(fixtures[("small", version)].stat().st_size, 256 << 10)
                self.assertEqual(fixtures[("large", version)].stat().st_size, acceptance.CHUNK + 17)
            self.assertNotEqual(acceptance.file_hash(fixtures[("large", 1)]),
                                acceptance.file_hash(fixtures[("large", 2)]))

    def test_mixed_and_unique_requests_check_each_real_response(self):
        with tempfile.TemporaryDirectory() as directory, ExitStack() as cleanup:
            root = Path(directory)
            fixtures = acceptance.create_fixtures(root / "fixtures", acceptance.CHUNK + 17)
            origin = acceptance.Origin(fixtures, root / "origin.json")
            cleanup.callback(origin.close)
            (root / "process.log").touch()
            client = acceptance.Client(fixtures, root)
            cleanup.callback(client.raw.close)
            product = SimpleNamespace(kind="test-origin", port=origin.server.server_port, dir=root,
                resource=lambda: {"time": 0, "cpu_seconds": 0, "rss_bytes": 0, "fds": 0,
                                  "read_bytes": 0, "write_bytes": 0})
            mixed = client.batch(product, origin, "mixed", "/small-mixed", 1, count=2,
                                 paths=["/small-mixed", "/large-mixed"])
            self.assertEqual(mixed["object_requests"], {"small": 1, "large": 1})
            self.assertEqual(mixed["unexpected_errors"], 0)
            unique = client.batch(product, origin, "unique", "/small-unique", 2, count=2, unique_paths=True)
            self.assertEqual(unique["origin"]["get"], 4)
            self.assertEqual(len({row["path"] for row in origin.records if "unique" in row["path"]}), 4)
            self.assertEqual(unique["unexpected_errors"], 0)


if __name__ == "__main__":
    unittest.main()
