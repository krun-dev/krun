import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("render-online-migration.py")
spec = importlib.util.spec_from_file_location("render_online_migration", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class RenderOnlineMigrationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.old_dir = module.MIGRATIONS
        self.addCleanup(setattr, module, "MIGRATIONS", self.old_dir)
        module.MIGRATIONS = Path(self.temp.name)
        for number in range(1, 2):
            (module.MIGRATIONS / f"{number:04d}_legacy.sql").write_text(f"-- {number}\n")

    def test_script_guards_all_history_and_records_checksum(self):
        target = module.MIGRATIONS / "0002_online_from_0001_example.sql"
        target.write_text("CREATE TABLE example (value text);\n")
        sql = module.render(target.name)
        self.assertIn("LOCK TABLE krun_schema_migrations", sql)
        self.assertIn("migration history does not match", sql)
        for path in sorted(module.MIGRATIONS.glob("*.sql")):
            self.assertIn(hashlib.sha256(path.read_bytes()).hexdigest(), sql)
        self.assertIn("CREATE TABLE example", sql)
        self.assertLess(sql.index("CREATE TABLE example"), sql.index("INSERT INTO krun_schema_migrations"))
        self.assertTrue(sql.rstrip().endswith("COMMIT;"))

    def test_rejects_unmarked_and_incompatible_migrations(self):
        for name in ("0002_unmarked.sql", "0002_online_from_0000_bad.sql", "0002_online_from_0002_bad.sql"):
            path = module.MIGRATIONS / name
            path.write_text("SELECT 1;\n")
            with self.assertRaises(ValueError):
                module.render(name)
            path.unlink()

    def test_rejects_missing_predecessor(self):
        (module.MIGRATIONS / "0001_legacy.sql").unlink()
        (module.MIGRATIONS / "0003_online_from_0001_example.sql").write_text("SELECT 1;\n")
        with self.assertRaises(ValueError):
            module.render("0003_online_from_0001_example.sql")


if __name__ == "__main__":
    unittest.main()
