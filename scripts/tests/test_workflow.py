import json
import os
import signal
from pathlib import Path
import shutil
import subprocess
import sys
import time
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
PROJECT = SCRIPTS.parent
sys.path.insert(0, str(SCRIPTS))
from agent_workflow import source_state


class Repository(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="billionaire-workflow-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "scripts" / "tests").mkdir(parents=True)
        for name in ("workspace", "check", "agent_workflow.py"):
            shutil.copy2(SCRIPTS / name, self.root / "scripts" / name)
        (self.root / "scripts" / "tests" / "__init__.py").write_text("")
        (self.root / "scripts" / "tests" / "test_fixture.py").write_text(
            "import unittest\nfrom pathlib import Path\n"
            "class Fixture(unittest.TestCase):\n"
            "    def test_runner_working_directory(self):\n"
            "        self.assertTrue(Path('source.txt').is_file())\n")
        (self.root / ".gitignore").write_text("/.scratch/\n/.worktrees/\n__pycache__/\n")
        (self.root / "compose.test.yaml").write_text("services: {}\n")
        (self.root / "source.txt").write_text("initial\n")
        self.git("init", "--initial-branch=main")
        self.git("config", "user.name", "Workflow fixture")
        self.git("config", "user.email", "workflow@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.hooksPath", str(self.root / ".git" / "hooks"))
        self.git("add", ".")
        self.git("commit", "-m", "fixture")

    def git(self, *args, cwd=None):
        return subprocess.check_output(["git", *args], cwd=cwd or self.root, stderr=subprocess.STDOUT, text=True).strip()

    def workspace(self, *args, success=True):
        result = subprocess.run([sys.executable, "scripts/workspace", *args], cwd=self.root, capture_output=True, text=True)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(result.stdout)
        self.assertNotEqual(result.returncode, 0)
        return result

    def test_checkpoint_retains_dirty_work_and_dependency_pointers(self):
        state = self.workspace("create", "ticket-29", "--ticket", "#29", "--blocked-by", "#24")
        tree = self.root / ".worktrees" / "ticket-29"
        self.assertEqual(state["blocked_by"], ["#24"])
        self.assertEqual(state["branch"], "agent/ticket-29")
        (tree / "source.txt").write_text("unfinished work\n")
        state = self.workspace("checkpoint", "ticket-29", "--note", "See docs/roadmap.md")
        self.assertTrue(state["dirty"])
        self.assertEqual(state["verification"]["status"], "unverified")
        self.assertEqual(self.workspace("status", "ticket-29")["source_digest"], state["source_digest"])
        self.assertEqual((tree / "source.txt").read_text(), "unfinished work\n")
        result = subprocess.check_output([sys.executable, "scripts/workspace", "status", "ticket-29"], cwd=tree, text=True)
        self.assertEqual(json.loads(result)["note"], "See docs/roadmap.md")

    def test_recovery_restores_missing_checkpoint_branch(self):
        state = self.workspace("create", "missing-branch", "--ticket", "#25")
        tree = self.root / ".worktrees" / "missing-branch"
        shutil.rmtree(tree)
        self.git("worktree", "prune")
        self.git("branch", "-D", state["branch"])
        restored = self.workspace("recover", "missing-branch")
        self.assertEqual(restored["commit"], state["commit"])
        self.assertEqual(self.git("branch", "--show-current"), "main")

    def test_existing_context_directory_is_reserved(self):
        context = self.root / ".scratch" / "runs" / "reserved"
        context.mkdir(parents=True)
        (context / "notes.md").write_text("keep context")
        self.workspace("create", "reserved", "--ticket", "#25", success=False)
        self.assertEqual((context / "notes.md").read_text(), "keep context")
        self.assertEqual(self.git("branch", "--list", "agent/reserved"), "")

    def test_recovery_rebuilds_checkpoint_without_resetting_branch(self):
        self.workspace("create", "recovery", "--ticket", "#25")
        tree = self.root / ".worktrees" / "recovery"
        (tree / "source.txt").write_text("checkpointed\n")
        self.git("add", "source.txt", cwd=tree)
        self.git("commit", "-m", "checkpoint", cwd=tree)
        state = self.workspace("checkpoint", "recovery")
        shutil.rmtree(tree)
        self.assertEqual(self.workspace("status", "recovery")["status"], "missing_worktree")
        restored = self.workspace("recover", "recovery")
        self.assertEqual(restored["commit"], state["commit"])
        self.assertEqual((tree / "source.txt").read_text(), "checkpointed\n")

    def test_unsafe_names_and_wrong_branch_are_refused(self):
        self.workspace("create", "../escape", "--ticket", "#25", success=False)
        self.workspace("create", "safe", "--ticket", "#25")
        self.git("switch", "--detach", cwd=self.root / ".worktrees" / "safe")
        self.workspace("status", "safe", success=False)
        self.workspace("create", "existing", "--ticket", "#25", "--branch", "main", success=False)
        tree = self.root / ".worktrees" / "safe"
        self.git("switch", "-c", "other", cwd=tree)
        self.workspace("checkpoint", "safe", success=False)
        self.assertEqual(self.git("branch", "--show-current", cwd=tree), "other")

    def test_fingerprint_tracks_sources_and_survives_commit(self):
        original = source_state(self.root)["source_digest"]
        (self.root / ".scratch").mkdir()
        (self.root / ".scratch" / "log").write_text("output\n")
        self.assertEqual(source_state(self.root)["source_digest"], original)
        (self.root / "new.txt").write_text("new\n")
        changed = source_state(self.root)["source_digest"]
        self.assertNotEqual(changed, original)
        self.git("add", "new.txt")
        self.git("commit", "-m", "new source")
        self.assertEqual(source_state(self.root)["source_digest"], changed)
        (self.root / "source.txt").unlink()
        self.assertNotEqual(source_state(self.root)["source_digest"], changed)

    def fake_tools(self):
        tools = self.root / ".scratch" / "fake-tools"
        tools.mkdir(parents=True)
        docker = tools / "docker"
        docker.write_text("""#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ['FIXTURE_DOCKER_LOG'], 'a') as log:
    log.write(json.dumps(args) + '\\n')
if 'port' in args: print('' if os.environ.get('FIXTURE_EMPTY_PORT') else '127.0.0.1:55432')
elif 'ps' in args: print('fixture-container')
elif args[0] == 'inspect': print('sha256:fixture-image')
elif 'down' in args and os.environ.get('FIXTURE_CLEANUP_FAIL'): sys.exit(1)
""")
        go = tools / "go"
        go.write_text("""#!/usr/bin/env python3
import os, sys, time
from pathlib import Path
if sys.argv[1:] == ['version']:
    print('go version go-fixture linux/amd64')
elif sys.argv[1] == 'test':
    assert os.environ.get('BILLIONAIRE_TEST_DATABASE_URL') == 'postgres://billionaire_test@127.0.0.1:55432/billionaire_test?sslmode=disable'
    assert os.environ.get('BILLIONAIRE_REQUIRE_TEST_DATABASE') == '1'
    assert 'BILLIONAIRE_DATABASE_URL' not in os.environ
    assert '-race' in sys.argv
    if os.environ.get('FIXTURE_BLOCK'):
        Path(os.environ['FIXTURE_BLOCK']).write_text('started')
        time.sleep(30)
    if os.environ.get('FIXTURE_CHANGE_SOURCE'): Path('source.txt').write_text('changed during checks')
    if os.environ.get('FIXTURE_TEST_FAIL'): sys.exit(1)
""")
        docker.chmod(0o755)
        go.chmod(0o755)
        return dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                    FIXTURE_DOCKER_LOG=str(self.root / ".scratch" / "docker.log"),
                    BILLIONAIRE_DATABASE_URL="must-not-be-used")

    def check(self, env):
        result = subprocess.run([sys.executable, "scripts/check"], cwd=self.root, env=env, capture_output=True, text=True)
        reports = list((self.root / ".scratch" / "checks").glob("*/result.json"))
        self.assertEqual(len(reports), 1, result.stderr)
        record = json.loads(reports[0].read_text())
        calls = [json.loads(line) for line in Path(env["FIXTURE_DOCKER_LOG"]).read_text().splitlines()]
        self.assertTrue(any("down" in call and "--volumes" in call for call in calls))
        return result, record

    def test_runner_records_database_race_checks_and_matching_success(self):
        env = self.fake_tools()
        result, record = self.check(env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(record["status"], "passed")
        self.assertEqual(record["postgres_image"], "sha256:fixture-image")
        status = subprocess.check_output([sys.executable, "scripts/check", "--status"], cwd=self.root, env=env, text=True)
        self.assertEqual(json.loads(status)["status"], "passed")
        changed_env = dict(env, GOFLAGS="-tags=changed-profile")
        status = subprocess.check_output([sys.executable, "scripts/check", "--status"], cwd=self.root, env=changed_env, text=True)
        self.assertEqual(json.loads(status)["status"], "unverified")
        (self.root / "source.txt").write_text("later edits")
        status = subprocess.check_output([sys.executable, "scripts/check", "--status"], cwd=self.root, env=env, text=True)
        self.assertEqual(json.loads(status)["status"], "unverified")

    def test_failure_always_cleans_up_and_cannot_record_success(self):
        env = self.fake_tools()
        env["FIXTURE_TEST_FAIL"] = "1"
        result, record = self.check(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(record["status"], "failed")
        self.assertEqual(record["cleanup"], "passed")

    def test_source_change_during_checks_invalidates_result(self):
        env = self.fake_tools()
        env["FIXTURE_CHANGE_SOURCE"] = "1"
        result, record = self.check(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(record["status"], "stale")

    def test_cleanup_failure_cannot_record_success(self):
        env = self.fake_tools()
        env["FIXTURE_CLEANUP_FAIL"] = "1"
        result, record = self.check(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(record["status"], "failed")
        self.assertEqual(record["cleanup"], "failed")

    def test_latest_record_uses_start_time_not_random_suffix(self):
        env = self.fake_tools()
        result, record = self.check(env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        existing = next((self.root / ".scratch" / "checks").iterdir())
        existing.rename(existing.with_name("20261119T100000Z-zzzz"))
        older = existing.with_name("20261119T100000Z-zzzz") / "result.json"
        record["started_at"] = "2026-11-19T10:00:00.000000+00:00"
        older.write_text(json.dumps(record))
        newer = existing.with_name("20261119T100000Z-aaaa")
        newer.mkdir()
        record.update(status="failed", started_at="2026-11-19T10:00:00.000001+00:00")
        (newer / "result.json").write_text(json.dumps(record))
        status = subprocess.check_output([sys.executable, "scripts/check", "--status"], cwd=self.root, env=env, text=True)
        self.assertEqual(json.loads(status)["status"], "failed")

    def test_missing_published_port_records_failure(self):
        env = self.fake_tools()
        env["FIXTURE_EMPTY_PORT"] = "1"
        result, record = self.check(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(record["status"], "failed")
        self.assertEqual(record["cleanup"], "passed")

    def test_interruption_preserves_record_and_cleans_up(self):
        env = self.fake_tools()
        marker = self.root / ".scratch" / "blocked"
        env["FIXTURE_BLOCK"] = str(marker)
        process = subprocess.Popen([sys.executable, "scripts/check"], cwd=self.root, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        try:
            deadline = time.monotonic() + 10
            while not marker.exists() and process.poll() is None and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertTrue(marker.exists(), "check did not reach the blocking test fixture")
            process.send_signal(signal.SIGTERM)
            output, _ = process.communicate(timeout=15)
            self.assertNotEqual(process.returncode, 0, output)
            records = list((self.root / ".scratch" / "checks").glob("*/result.json"))
            record = json.loads(records[0].read_text())
            self.assertEqual(record["status"], "interrupted")
            self.assertEqual(record["cleanup"], "passed")
            self.assertEqual(record["phases"][-1]["status"], "interrupted")
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()


class RequiredDatabaseCoverage(unittest.TestCase):
    def test_missing_required_database_fails_instead_of_skipping(self):
        env = dict(os.environ, BILLIONAIRE_REQUIRE_TEST_DATABASE="1")
        env.pop("BILLIONAIRE_TEST_DATABASE_URL", None)
        result = subprocess.run(["go", "test", "./internal/store/postgres", "-run", "^TestNotificationTargetCooldownDefersBatchAndSurvivesRestart$", "-count=1"], cwd=PROJECT, env=env, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("PostgreSQL coverage required", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
