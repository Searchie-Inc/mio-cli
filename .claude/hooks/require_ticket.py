#!/usr/bin/env python3
"""Preflight code commits; Go tests count as code, docs/YAML/scripts do not."""
import json
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path

TICKET = re.compile(r'MIO-[0-9]+')


def fail(message):
    print('Commit guard: ' + message, file=sys.stderr)
    raise SystemExit(2)


def segments(command):
    # Replace heredocs before shell splitting. Keep their text attached to the
    # command segment, including heredocs nested in command substitutions.
    bodies = []
    pattern = re.compile(r'<<-?\s*([\'\"]?)([A-Za-z_][A-Za-z_0-9]*)\1[^\n]*\n')
    while True:
        match = pattern.search(command)
        if not match:
            break
        end = re.search(r'(?m)^\s*' + re.escape(match.group(2)) + r'\s*$', command[match.end():])
        if not end:
            raise ValueError('unterminated heredoc')
        start = match.end()
        stop = start + end.end()
        bodies.append(command[start:start + end.start()])
        # Preserve the line suffix (usually the closing parenthesis/quote).
        command = command[:match.start()] + ' HEREDOC ' + command[stop:]
    result = []
    start = 0
    quote = None
    depth = 0
    i = 0
    while i < len(command):
        c = command[i]
        if c == '\\' and quote != "'":
            i += 2; continue
        if quote:
            if c == quote: quote = None
        elif c in "'\"": quote = c
        elif command[i:i+2] == '$(': depth += 1; i += 1
        elif c == ')' and depth: depth -= 1
        elif depth == 0 and c in ';&|\n':
            result.append(command[start:i]); start = i + 1
        i += 1
    if quote or depth: raise ValueError('unclosed quote or substitution')
    result.append(command[start:])
    return [(part, bodies if 'HEREDOC' in part else []) for part in result if part.strip()]


def git(repo, *args):
    p = subprocess.run(['git', *args], cwd=repo, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if p.returncode:
        raise ValueError('git ' + ' '.join(args) + ' failed: ' + p.stderr.decode(errors='replace').strip())
    return p.stdout.decode(errors='replace')


def paths(repo, *args):
    return set(filter(None, git(repo, *args).split('\0')))


def status_paths(repo, tracked=False):
    out = git(repo, 'status', '--porcelain=v1', '-z', '--untracked-files=all')
    records = out.split('\0'); result = set(); i = 0
    while i < len(records) and records[i]:
        record = records[i]; i += 1
        if not tracked or record[:2] != '??': result.add(record[3:])
        if 'R' in record[:2] or 'C' in record[:2]: i += 1
    return result


def unwrap(tokens):
    while tokens:
        name = os.path.basename(tokens[0])
        if re.match(r'^[A-Za-z_][A-Za-z_0-9]*=', tokens[0]): tokens = tokens[1:]; continue
        if name in ('env', 'command', 'exec', 'time', 'sudo', 'nohup'):
            tokens = tokens[1:]
            while tokens and (tokens[0].startswith('-') or re.match(r'^[A-Za-z_][A-Za-z_0-9]*=', tokens[0])):
                opt = tokens.pop(0)
                if opt in ('-u', '--unset', '-C', '--chdir', '-g', '-h', '-p', '-u') and tokens: tokens.pop(0)
            continue
        if name == 'xargs':
            tokens = tokens[1:]
            while tokens and tokens[0].startswith('-'):
                opt = tokens.pop(0)
                if opt in ('-I', '-i', '-n', '-P', '-s', '-L', '-d', '-E', '--max-args', '--max-procs', '--replace') and tokens: tokens.pop(0)
            continue
        break
    return tokens


def git_command(tokens, cwd):
    tokens = unwrap(tokens)
    if not tokens: return None
    if os.path.basename(tokens[0]) in ('bash', 'sh', 'zsh'):
        for i, token in enumerate(tokens[1:], 1):
            if token == '-c' and i + 1 < len(tokens): return ('shell', cwd, tokens[i+1])
        return None
    if os.path.basename(tokens[0]) != 'git': return None
    repo = Path(cwd); i = 1
    while i < len(tokens):
        t = tokens[i]
        if t in ('-C', '-c', '--git-dir', '--work-tree', '--namespace', '--config-env'):
            if i + 1 >= len(tokens): raise ValueError('missing git global option argument')
            if t == '-C': repo = repo / tokens[i+1]
            i += 2
        elif t.startswith('-C') and t != '-C': repo = repo / t[2:]; i += 1
        elif t.startswith('-'): i += 1
        else: break
    if i >= len(tokens): return None
    return (tokens[i], str(repo.resolve()), tokens[i+1:])


def add_paths(repo, args):
    if any(x in ('-A', '--all', '-u', '--update', '.', '--no-ignore-removal') for x in args):
        return status_paths(repo)
    specs = [x for x in args if not x.startswith('-')]
    if not specs: return set()
    # ls-files handles directories, globs and deleted tracked paths.
    return paths(repo, 'ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', *specs) & status_paths(repo)


def commit_details(repo, args, bodies, added):
    files = paths(repo, 'diff', '--cached', '--name-only', '-z') | added
    messages = list(bodies); specs = []; all_flag = False; amend = False; reuse = False
    i = 0
    while i < len(args):
        t = args[i]; i += 1
        if t == '--': specs.extend(args[i:]); break
        if t in ('-m', '--message', '-F', '--file', '-C', '-c', '--reuse-message', '--reedit-message'):
            if i >= len(args): raise ValueError('missing commit option argument: ' + t)
            value = args[i]; i += 1
            if t in ('-m', '--message'): messages.append(value)
            elif t in ('-F', '--file'): messages.append(Path(repo, value).read_text())
            else: messages.append(git(repo, 'log', '-1', '--format=%B', value))
        elif t.startswith('--message='): messages.append(t.split('=', 1)[1])
        elif t.startswith('--file='): messages.append(Path(repo, t.split('=', 1)[1]).read_text())
        elif t.startswith('-m') and len(t) > 2: messages.append(t[2:])
        elif t.startswith('-am') and len(t) > 3: all_flag = True; messages.append(t[3:])
        elif t.startswith('-') and not t.startswith('--') and 'm' in t[1:]:
            all_flag |= 'a' in t[1:t.index('m')]
            if i >= len(args): raise ValueError('missing commit message')
            messages.append(args[i]); i += 1
        elif t in ('-a', '--all') or (t.startswith('-') and not t.startswith('--') and 'a' in t[1:]): all_flag = True
        elif t == '--amend': amend = True
        elif t == '--no-edit': reuse = True
        elif not t.startswith('-'): specs.append(t)
    if all_flag: files |= status_paths(repo, tracked=True)
    if specs: files |= paths(repo, 'ls-files', '-z', '--cached', '--', *specs)
    if amend: files |= paths(repo, 'diff-tree', '--root', '--no-commit-id', '--name-only', '-r', '-z', 'HEAD')
    if (amend or reuse) and not messages: messages.append(git(repo, 'log', '-1', '--format=%B'))
    return files, '\n'.join(messages)


def inspect(command, cwd, pending, depth=0):
    if depth > 12: raise ValueError('wrapper nesting too deep')
    for segment, bodies in segments(command):
        tokens = shlex.split(segment)
        parsed = git_command(tokens, cwd)
        if not parsed: continue
        verb, repo, args = parsed
        if verb == 'shell': inspect(args, repo, pending, depth + 1)
        elif verb == 'add': pending.setdefault(repo, set()).update(add_paths(repo, args))
        elif verb == 'commit':
            files, message = commit_details(repo, args, bodies, pending.get(repo, set()))
            code = sorted(p for p in files if p.endswith('.go') or Path(p).name in ('go.mod', 'go.sum'))
            if code and not TICKET.search(message):
                fail('code commit detected: ' + ', '.join(code) + '. Go test files count as code. Include a ticket in the commit message, for example "MIO-1234: describe change" or "(MIO-1234)".')


def main():
    raw = sys.stdin.read()
    try:
        payload = json.loads(raw)
        if payload['tool_name'] != 'Bash': return
        command = payload['tool_input']['command']; cwd = payload['cwd']
        if not isinstance(command, str) or not isinstance(cwd, str): raise ValueError('invalid payload fields')
        inspect(command, cwd, {})
    except Exception as exc:
        if 'git' in raw and 'commit' in raw: fail('cannot parse or inspect this git commit: ' + str(exc))


if __name__ == '__main__': main()
