import os
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
                args = SimpleNamespace(binary=Path(directory) / "missing-binary")
                with patch.object(acceptance, "Origin", origin), patch.object(acceptance, "Client", client):
                    with self.assertRaises(FileNotFoundError):
                        run(args, {}, Path(directory))
                self.assertTrue(all(not instance.thread.is_alive() for instance in origins))
                self.assertTrue(all(instance.server.fileno() == -1 for instance in origins))
                self.assertTrue(all(instance.raw.closed for instance in clients))


if __name__ == "__main__":
    unittest.main()
