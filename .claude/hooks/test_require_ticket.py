import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
FIXTURE = json.loads((HERE / 'testdata/real-pretooluse-bash-payload.json').read_text())
HOOK = HERE / 'require-ticket.sh'

class GuardTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name) / 'repo'
        self.repo.mkdir()
        self.git('init', '-q')
        self.git('config', 'user.name', 'Test')
        self.git('config', 'user.email', 'test@example.com')
        self.write('cmd/x.go', 'package cmd\n')
        self.write('docs/readme.md', 'hello\n')
        self.git('add', '.')
        self.git('commit', '-qm', 'MIO-99: initial')

    def git(self, *args, cwd=None):
        return subprocess.run(['git', *args], cwd=cwd or self.repo, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True).stdout

    def write(self, path, content):
        target = self.repo / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)

    def check_hook(self, command, code, phrase=None, cwd=None, env=None, raw=None, tool='Bash'):
        payload = copy.deepcopy(FIXTURE)
        payload['cwd'] = str(cwd or self.repo)
        payload['tool_name'] = tool
        payload['tool_input']['command'] = command
        result = subprocess.run([str(HOOK)], input=raw if raw is not None else json.dumps(payload), text=True, capture_output=True, env=env)
        self.assertEqual(result.returncode, code, result.stderr)
        if phrase:
            self.assertIn(phrase, result.stderr)

    def modified(self):
        self.write('cmd/x.go', 'package changed\n')

    def staged(self):
        self.modified()
        self.git('add', 'cmd/x.go')

    def test_test_file_is_code(self):
        self.write('cmd/x_test.go', 'package cmd\n')
        self.check_hook('git add cmd/x_test.go && git commit -m tidy', 2, 'test files count as code')

    def test_add_directory_unchanged_code(self):
        self.write('docs/readme.md', 'changed')
        self.check_hook('git add . && git commit -m tidy', 0)

    def test_message_file(self):
        self.staged(); self.write('message.txt', 'MIO-12: change')
        self.check_hook('git commit -F message.txt', 0)

    def test_bad_git_fails_closed(self):
        self.check_hook('git -C missing commit -m tidy', 2, 'cannot parse or inspect')

    def test_fixture_schema(self):
        self.assertEqual(FIXTURE['tool_name'], 'Bash')
        self.assertIsInstance(FIXTURE['tool_input']['command'], str)
        self.assertIsInstance(FIXTURE['cwd'], str)

    def test_add_path(self):
        self.modified(); self.check_hook('git add cmd/x.go && git commit -m tidy', 2, 'test files count as code')
    def test_add_all(self):
        self.modified(); self.check_hook('git add -A && git commit -m tidy', 2, 'cmd/x.go')
    def test_all_flag(self):
        self.modified(); self.check_hook('git commit -a -m tidy', 2, 'cmd/x.go')
    def test_am_flag(self):
        self.modified(); self.check_hook('git commit -am tidy', 2, 'cmd/x.go')
    def test_pathspec(self):
        self.modified(); self.check_hook('git commit cmd/x.go -m tidy', 2, 'cmd/x.go')
    def test_heredoc_without_ticket(self):
        self.staged(); self.check_hook("git commit -m \"$(cat <<'EOF'\nchore: tidy\nEOF\n)\"", 2, 'cmd/x.go')
    def test_heredoc_ticket(self):
        self.staged(); self.check_hook("git commit -m \"$(cat <<'EOF'\nchore: tidy\n\nMIO-123\nEOF\n)\"", 0)
    def test_bash_wrapper(self):
        self.staged(); self.check_hook('bash -c "git commit -m tidy"', 2, 'cmd/x.go')
    def test_env_wrapper(self):
        self.staged(); self.check_hook('env git commit -m tidy', 2, 'cmd/x.go')
    def test_xargs_wrapper(self):
        self.staged(); self.check_hook('echo x | xargs -I{} git commit -m {}', 2, 'cmd/x.go')
    def test_docs_only(self):
        self.write('docs/readme.md', 'changed'); self.git('add', '.'); self.check_hook('git commit -m tidy', 0)
    def test_go_mod_and_go_sum_are_code(self):
        # The guard's header says go.mod / go.sum are code; a version that only
        # matched *.go would let a dependency change through without a ticket.
        for name in ('go.mod', 'go.sum'):
            with self.subTest(name=name):
                self.write(name, 'changed ' + name); self.git('add', name)
                self.check_hook('git commit -m tidy', 2, name)
                self.git('reset', '-q', 'HEAD', '--', name)
    def test_non_commands(self):
        self.staged()
        for cmd in ['grep -rn "git commit" .', 'git log --grep="git commit"', 'echo "git commit"']:
            with self.subTest(cmd=cmd): self.check_hook(cmd, 0)
    def test_git_global_option(self):
        self.staged(); self.check_hook('git -c user.name=a commit -m tidy', 2, 'cmd/x.go')
    def test_git_path(self):
        self.staged(); self.check_hook('/usr/bin/git commit -m tidy', 2, 'cmd/x.go')
    def test_chain(self):
        self.staged(); self.check_hook('echo ok && git commit -m tidy', 2, 'cmd/x.go')
    def test_ticket(self):
        self.staged(); self.check_hook('git commit -m "MIO-123: tidy"', 0)
    def test_ticket_in_path(self):
        self.staged(); self.write('docs/MIO-1.md', 'doc'); self.check_hook('git add docs/MIO-1.md && git commit -m tidy', 2, 'cmd/x.go')
    def test_amend_no_edit_ticket(self):
        self.check_hook('git commit --amend --no-edit', 0)
    def test_amend_no_edit_no_ticket(self):
        self.git('commit', '--amend', '-qm', 'tidy'); self.check_hook('git commit --amend --no-edit', 2, 'cmd/x.go')
    def test_amend_clean_retitle(self):
        self.check_hook('git commit --amend -m tidy', 2, 'cmd/x.go')
    def test_other_repo(self):
        other = Path(self.tmp.name) / 'other'; other.mkdir()
        self.git('init', '-q', cwd=other); self.git('config', 'user.name', 'Test', cwd=other); self.git('config', 'user.email', 't@x', cwd=other)
        (other / 'README.md').write_text('x'); self.git('add', '.', cwd=other); self.git('commit', '-qm', 'init', cwd=other)
        self.staged()
        self.check_hook('git -C ../repo commit -m tidy', 2, 'cmd/x.go', cwd=other)
        self.check_hook('git -C ../other commit -m tidy', 0, cwd=self.repo)
    def test_malformed(self):
        self.check_hook('', 2, 'parse', raw='{ "git commit"')
        self.check_hook('', 0, raw='{ hello')
    def test_non_bash(self):
        self.staged(); self.check_hook('git commit -m tidy', 0, tool='Read')
    def test_no_commit_skips_python(self):
        bindir = Path(self.tmp.name) / 'fakebin'; bindir.mkdir()
        os.symlink('/usr/bin/cat', bindir / 'cat')
        marker = bindir / 'called'
        fake = bindir / 'python3'
        fake.write_text('#!/bin/sh\ntouch "' + str(marker) + '"\nexit 99\n')
        fake.chmod(0o755)
        self.check_hook('echo hello', 0, env=dict(os.environ, PATH=str(bindir)))
        self.assertFalse(marker.exists())

    def test_missing_python(self):
        bindir = Path(self.tmp.name) / 'bin'; bindir.mkdir()
        for executable in ('sh', 'git', 'cat'):
            os.symlink('/usr/bin/' + executable, bindir / executable)
        env = dict(os.environ, PATH=str(bindir))
        self.check_hook('git commit -m tidy', 2, 'python3', env=env)
        self.check_hook('echo hello', 0, env=env)

if __name__ == '__main__': unittest.main()
