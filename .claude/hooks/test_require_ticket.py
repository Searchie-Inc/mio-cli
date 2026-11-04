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
        self.check_hook('', 0, raw='{ git commit MIO-1')
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

    def test_wrapper_forms(self):
        self.staged()
        for cmd in ["(git commit -m x)", "{ git commit -m x; }", "echo $(git commit -m x)", "echo `git commit -m x`", 'eval "git commit -m x"', 'if true; then git commit -m x; fi', 'for i in 1; do git commit -m x; done', '! git commit -m x', 'timeout 5 git commit -m x', 'nice git commit -m x', 'nohup git commit -m x', 'sudo git commit -m x', 'command git commit -m x', 'exec git commit -m x', 'time git commit -m x', "bash -lc 'git commit -m x'", "bash -ec 'git commit -m x'"]:
            with self.subTest(cmd=cmd): self.check_hook(cmd, 2)

    def test_unmodelled_steps(self):
        for step in ['git mv a b', 'git rm a', 'git checkout -- .', 'git stash pop', 'git merge --squash f', 'git revert HEAD -n', 'gofmt -w cmd/x.go', 'sed -i s/a/b/ x', 'echo hi > new.go']:
            with self.subTest(step=step):
                self.check_hook(step + ' && git add -A && git commit -m x', 2)
                self.check_hook(step + ' && git add -A && git commit -m MIO-1', 0)

    def test_redirection_and_sed_mixed_options_are_unmodelled(self):
        for step in ['git status > new.go', 'sed -n -i s/a/b/ x']:
            with self.subTest(step=step):
                self.check_hook(step + ' && git commit -m x', 2, 'unmodelled')
                self.check_hook(step + ' && git commit -m MIO-1', 0)

    def test_add_clusters_and_stage(self):
        self.modified()
        for add in ['git stage cmd/x.go', 'git add -fA', 'git add -vu', 'git add -Av']:
            with self.subTest(add=add): self.check_hook(add + ' && git commit -m x', 2)

    def test_stage_docs_is_modelled(self):
        self.write('docs/readme.md', 'changed')
        self.check_hook('git stage docs/readme.md && git commit -m docs', 0)

    def test_ticket_sources_and_boundaries(self):
        self.staged()
        for cmd in ["git commit -F - <<'EOF'\nMIO-1: x\nEOF", 'echo "MIO-1: x" | git commit -F -', "printf 'MIO-1: x' > /tmp/m && git commit -F /tmp/m", 'git -C ~/missing commit -m "MIO-1: x"', 'git -C $PWD commit -m "MIO-1: x"', "git commit --trailer 'Refs: MIO-5'", 'git commit --message=MIO-1', 'git commit -mMIO-1', 'git commit -amMIO-1', 'git commit -sam MIO-1', 'git commit -C HEAD', 'git commit --fixup HEAD']:
            with self.subTest(cmd=cmd): self.check_hook(cmd, 0)
        for cmd in ['git commit -m XMIO-12', 'git commit -m mio-12', "cat <<'EOF' | git commit -F -\nchore: x\nEOF"]:
            with self.subTest(cmd=cmd): self.check_hook(cmd, 2)
        # Known limitation: any heredoc attached to the commit contributes text.
        self.check_hook('git commit -m tidy <<EOF\nMIO-1\nEOF', 0)

    def test_cd_tracking(self):
        other = Path(self.tmp.name) / 'other'; other.mkdir()
        self.git('init', '-q', cwd=other); self.git('config', 'user.name', 'T', cwd=other); self.git('config', 'user.email', 't@x', cwd=other)
        (other / 'README').write_text('x'); self.git('add', '.', cwd=other); self.git('commit', '-qm', 'init', cwd=other)
        self.staged()
        self.check_hook('cd ' + str(self.repo) + ' && git commit -m x', 2, cwd=other)
        self.check_hook('cd ' + str(other) + ' && git commit -m x', 0)
        self.check_hook('cd missing && git commit -m x', 2)
        self.check_hook('cd missing && git commit -m x', 2, cwd=other)
        self.check_hook('cd - && git commit -m x', 2, cwd=other)
        self.git('reset', '-q', 'HEAD', '--', 'cmd/x.go')
        self.check_hook('cd cmd && git add x.go && git commit -m x', 2)

    def test_rename_go_to_docs_with_all(self):
        self.git('mv', 'cmd/x.go', 'docs/x.txt')
        self.check_hook('git commit -a -m tidy', 2, 'cmd/x.go')

    def test_untracked_go_not_in_commit_all(self):
        self.write('new.go', 'package x')
        self.check_hook('git commit -a -m tidy', 0)

    def test_wrapper_nesting_limit(self):
        command = 'git commit -m tidy'
        for _ in range(14):
            command = 'bash -c "' + command.replace('\\', '\\\\').replace('"', '\\"') + '"'
        self.check_hook(command, 2, 'nesting')

    def test_ticket_never_inspects_repo(self):
        bindir = Path(self.tmp.name) / 'fake-git'; bindir.mkdir()
        marker = bindir / 'called'
        fake = bindir / 'git'
        fake.write_text('#!/bin/sh\ntouch "' + str(marker) + '"\nexit 1\n')
        fake.chmod(0o755)
        env = dict(os.environ, PATH=str(bindir) + ':' + os.environ['PATH'])
        self.check_hook('git add -A && git commit -m MIO-1', 0, env=env)
        self.assertFalse(marker.exists(), 'ticketed commit inspected the repository')

    def test_ticket_after_uninspectable_add(self):
        self.check_hook('git add -p && git commit -m MIO-1', 0)

    def test_comments(self):
        self.check_hook("git status # don't commit yet", 0)
        self.check_hook("git commit -m x # don't forget", 0)

    def test_settings_wiring(self):
        root = HERE.parent.parent
        entries = json.loads((root / '.claude/settings.json').read_text())['hooks']['PreToolUse']
        self.assertEqual(len(entries), 1); self.assertEqual(entries[0]['matcher'], 'Bash')
        import shlex
        command = entries[0]['hooks'][0]['command'].replace('$CLAUDE_PROJECT_DIR', str(root))
        path = Path(shlex.split(command)[0]); self.assertEqual(path, HOOK); self.assertTrue(os.access(path, os.X_OK))

    def test_crashing_parser(self):
        bindir = Path(self.tmp.name) / 'fake'; bindir.mkdir()
        fake = bindir / 'python3'; fake.write_text('#!/bin/sh\nexit 3\n'); fake.chmod(0o755)
        env = dict(os.environ, PATH=str(bindir) + ':' + os.environ['PATH'])
        self.check_hook('git commit -m x', 2, 'parser failed', env=env)
        self.check_hook('git commit -m MIO-1', 0, env=env)
        self.check_hook('echo commit', 0, env=env)

if __name__ == '__main__': unittest.main()
