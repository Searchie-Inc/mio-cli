#!/usr/bin/env python3
"""Conservative preflight for ticketless code commits."""
import json
import glob
import os
import re
import shlex
import subprocess
import sys
from pathlib import Path

TICKET = re.compile(r'(?<![A-Za-z0-9])MIO-[0-9]+\b')


def fail(message):
    print('Commit guard: ' + message + '. Code = *.go/go.mod/go.sum; test files count as code. When the hook cannot tell it blocks; put a MIO-n key in the message.', file=sys.stderr)
    raise SystemExit(2)


def git(repo, *args):
    p = subprocess.run(['git', *args], cwd=repo, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if p.returncode:
        raise ValueError('git ' + ' '.join(args) + ' failed: ' + p.stderr.decode(errors='replace').strip())
    return p.stdout.decode(errors='replace')


def paths(repo, *args):
    return set(filter(None, git(repo, *args).split('\0')))


def status_paths(repo, tracked=False):
    records = git(repo, 'status', '--porcelain=v1', '-z', '--untracked-files=all').split('\0')
    result = set(); i = 0
    while i < len(records) and records[i]:
        record = records[i]; i += 1
        if not tracked or record[:2] != '??': result.add(record[3:])
        if 'R' in record[:2] or 'C' in record[:2]:
            if i < len(records) and records[i]: result.add(records[i])
            i += 1
    return result


def expand_dir(cwd, value):
    value = os.path.expanduser(os.path.expandvars(value))
    if re.search(r'\$|^~', value) or value == '-' or cwd is None: return None
    path = (Path(cwd) / value).resolve()
    return str(path) if path.is_dir() else None


def unwrap(tokens):
    tokens = tokens[:]
    while tokens:
        name = os.path.basename(tokens[0])
        if re.match(r'^[A-Za-z_][A-Za-z_0-9]*=', tokens[0]): tokens.pop(0); continue
        if name in ('env', 'command', 'exec', 'time', 'sudo', 'nohup', 'nice', 'timeout', 'xargs'):
            tokens.pop(0)
            while tokens and (tokens[0].startswith('-') or re.match(r'^[A-Za-z_][A-Za-z_0-9]*=', tokens[0])):
                opt = tokens.pop(0)
                if opt in ('-u', '--unset', '-C', '--chdir', '-g', '-h', '-p', '-I', '-i', '-n', '-P', '-s', '-L', '-d', '-E', '--max-args', '--max-procs', '--replace', '-n', '--adjustment') and tokens: tokens.pop(0)
            if name == 'timeout' and tokens: tokens.pop(0)
            continue
        break
    return tokens


def git_command(tokens, cwd):
    tokens = unwrap(tokens)
    if not tokens: return None
    name = os.path.basename(tokens[0])
    if name in ('bash', 'sh', 'zsh', 'dash'):
        for i, token in enumerate(tokens[1:], 1):
            if re.fullmatch(r'-[A-Za-z]*c', token) and i + 1 < len(tokens): return ('shell', cwd, tokens[i+1])
        return None
    if name == 'eval' and len(tokens) > 1: return ('shell', cwd, ' '.join(tokens[1:]))
    if name != 'git': return None
    repo = cwd; i = 1
    while i < len(tokens):
        t = tokens[i]
        if t in ('-C', '-c', '--git-dir', '--work-tree', '--namespace', '--config-env'):
            if i + 1 >= len(tokens): raise ValueError('missing git global option argument')
            if t == '-C': repo = expand_dir(repo, tokens[i+1])
            i += 2
        elif t.startswith('-C') and t != '-C': repo = expand_dir(repo, t[2:]); i += 1
        elif t.startswith('-'): i += 1
        else: break
    if i >= len(tokens): return None
    return (tokens[i], repo, tokens[i+1:])


def add_paths(repo, args):
    if any(x in ('-p', '-i', '--patch', '--interactive') or (x.startswith('-') and not x.startswith('--') and any(c in x[1:] for c in 'pi')) for x in args):
        raise ValueError('interactive git add')
    if any(x.startswith('--pathspec-from-file') for x in args): raise ValueError('unresolvable git add pathspec file')
    specs = [x for x in args if not x.startswith('-')]
    for spec in specs:
        if re.search(r'[$ {}\x60(]', spec) or 'SUBSTITUTION' in spec: raise ValueError('unresolvable git add pathspec')
        if glob.has_magic(spec) and not glob.glob(str(Path(repo) / spec), recursive=True): raise ValueError('unmatched git add glob')
    if any(x in ('--all', '--update', '.', ':/', '--no-ignore-removal', '-N') or (x.startswith('-') and not x.startswith('--') and any(c in x[1:] for c in 'AuN')) for x in args):
        return status_paths(repo)
    if not specs: return set()
    # Git returns root-relative names even when invoked from a subdirectory.
    return paths(repo, 'ls-files', '--full-name', '-z', '--cached', '--others', '--exclude-standard', '--', *specs) & status_paths(repo)


def message_file(repo, value, bodies, context, message_only):
    if value in ('-', '/dev/stdin'):
        if bodies: return bodies
        if context['pipe']: return [context['pipe']]
        if not message_only: raise ValueError('unresolvable commit stdin')
        return []
    try: return [Path(repo, value).read_text()]
    except (OSError, TypeError):
        if value in context['writes']: return [context['writes'][value]]
        if not message_only: raise ValueError('unresolvable commit message file: ' + value)
        return []


def commit_details(repo, args, bodies, command, added, message_only=False):
    messages = list(bodies); specs = []; all_flag = False; amend = False; reuse = []
    i = 0
    while i < len(args):
        t = args[i]; i += 1
        if t == '--': specs.extend(args[i:]); break
        if t in ('-m', '--message', '-F', '--file', '-C', '-c', '--reuse-message', '--reedit-message', '--trailer', '--author', '--date', '-t', '--template', '--fixup', '--squash'):
            if i >= len(args): raise ValueError('missing commit option argument: ' + t)
            value = args[i]; i += 1
            if t in ('-m', '--message', '--trailer'): messages.append(value)
            elif t in ('-F', '--file'):
                messages.extend(message_file(repo, value, bodies, command, message_only))
            elif t in ('-C', '-c', '--reuse-message', '--reedit-message', '--fixup', '--squash'): reuse.append(value.removeprefix('amend:').removeprefix('reword:'))
        elif t.startswith(('--message=', '--trailer=')): messages.append(t.split('=', 1)[1])
        elif t.startswith('--file='):
            messages.extend(message_file(repo, t.split('=', 1)[1], bodies, command, message_only))
        elif t.startswith(('--fixup=', '--squash=', '--reuse-message=', '--reedit-message=')): reuse.append(t.split('=', 1)[1].removeprefix('amend:').removeprefix('reword:'))
        elif t.startswith('-') and not t.startswith('--') and 'm' in t[1:]:
            pos = t.index('m'); all_flag |= 'a' in t[1:pos]
            if t[pos+1:]: messages.append(t[pos+1:])
            elif i < len(args): messages.append(args[i]); i += 1
            else: raise ValueError('missing commit message')
        elif t in ('-a', '--all') or (t.startswith('-') and not t.startswith('--') and 'a' in t[1:]): all_flag = True
        elif t == '--amend': amend = True
        elif t == '--no-edit': reuse.append('HEAD')
        elif not t.startswith('-'): specs.append(t)
    if amend and not messages: reuse.append('HEAD')
    if TICKET.search('\n'.join(messages)): return set(), True
    for rev in reuse:
        try: messages.append(git(repo, 'log', '-1', '--format=%B', rev))
        except (ValueError, TypeError, OSError): pass
    if TICKET.search('\n'.join(messages)): return set(), True
    if message_only: return set(), False
    if repo is None: raise ValueError('unresolvable cwd')
    files = paths(repo, 'diff', '--cached', '--no-renames', '--name-only', '-z') | added
    if all_flag: files |= status_paths(repo, tracked=True)
    if specs: files |= paths(repo, 'ls-files', '--full-name', '-z', '--cached', '--', *specs)
    if amend: files |= paths(repo, 'diff-tree', '--root', '--no-commit-id', '--name-only', '-r', '-z', 'HEAD')
    return files, False


def parse(command):
    bodies = {}
    pattern = re.compile(r"<<-?\s*(['\"]?)([A-Za-z_][A-Za-z_0-9]*)\1([^\n]*)\n")
    while (match := pattern.search(command)):
        end = re.search(r'(?m)^\s*' + re.escape(match.group(2)) + r'\s*$', command[match.end():])
        if not end: raise ValueError('unterminated heredoc')
        key = 'HEREDOC' + str(len(bodies)) + 'END'
        bodies[key] = command[match.end():match.end()+end.start()]
        command = command[:match.start()] + ' ' + key + match.group(3) + '\n' + command[match.end()+end.end():]
    substitutions = []
    def sub(m):
        value = m.group(1) or m.group(2); substitutions.append(value)
        text = ' '.join(bodies[k] for k in bodies if k in value)
        return 'SUBSTITUTION' + str(len(substitutions)-1) + 'END ' + text.replace(' ', '_')
    command = re.sub(r'\$\(([^()]*)\)|`([^`]*)`', sub, command)
    out = []; quote = None; i = 0
    while i < len(command):
        c = command[i]
        if c == "\\" and quote != "'" and i+1 < len(command):
            if command[i+1] == '\n': i += 2; continue
            if command[i+1:i+3] == '\r\n': i += 3; continue
            out.extend(command[i:i+2]); i += 2; continue
        if c in "'\"":
            if quote == c: quote = None
            elif quote is None: quote = c
        if c == '\n' and quote is None: out.append('\n;')
        elif c != '\r' or quote is not None: out.append(c)
        i += 1
    lexer = shlex.shlex(''.join(out), posix=True, punctuation_chars=';&|()<>')
    lexer.whitespace_split = True; lexer.commenters = '#'
    tokens = list(lexer); parts = []; part = []; separator = ';'
    for token in tokens:
        if token in ('{', '}') or (token and all(c in ';&|()' for c in token)):
            if part: parts.append((part, separator)); part = []
            separator = token
        else: part.append(token)
    if part: parts.append((part, separator))
    return parts, bodies, substitutions


def safe_redirects(tokens):
    for i, token in enumerate(tokens):
        if '>' not in token: continue
        target = tokens[i+1] if i+1 < len(tokens) else ''
        if target not in ('/dev/null', '/dev/stdout', '/dev/stderr') and not re.fullmatch(r'&?\d+', target): return False
    return True


def inspect(command, cwd, state, depth=0):
    if depth > 12: raise ValueError('wrapper nesting too deep')
    parts, bodies, substitutions = parse(command)
    for sub in substitutions: inspect(sub, cwd, state, depth + 1)
    pipeline = []; writes = {}
    for tokens, separator in parts:
        if separator != '|': pipeline = []
        while tokens and tokens[0] in ('if', 'then', 'else', 'elif', 'do', 'while', 'until', '!', 'fi', 'done'): tokens = tokens[1:]
        if not tokens: continue
        if tokens[0] == 'for': continue
        if tokens[0] == 'cd':
            cwd = expand_dir(cwd, tokens[1] if len(tokens) > 1 else os.path.expanduser('~')); continue
        parsed = git_command(tokens, cwd)
        local_bodies = [bodies[t] for t in tokens if t in bodies]
        for i, t in enumerate(tokens[:-1]):
            if t in ('>', '>>', 'tee'): writes[tokens[i+1]] = ' '.join(tokens)
        context = {'pipe': ' '.join(pipeline), 'writes': writes}
        if parsed:
            verb, repo, args = parsed
            if verb != 'commit' and not safe_redirects(tokens): state['uncertain'] = 'redirection'
            if verb == 'shell': inspect(args, repo, state, depth + 1); continue
            if verb == 'commit':
                if '--dry-run' in args: continue
                _, ticket = commit_details(repo, args, local_bodies, context, set(), True)
                if ticket: continue
                if state['uncertain']: fail('cannot parse or inspect: unmodelled step ' + state['uncertain'])
                added = set()
                for add_repo, add_args in state['adds']:
                    if add_repo is None: raise ValueError('unresolvable cwd for git add')
                    added.update(add_paths(add_repo, add_args))
                files, _ = commit_details(repo, args, local_bodies, context, added)
                code = sorted(p for p in files if p.endswith('.go') or Path(p).name in ('go.mod', 'go.sum'))
                if code: fail('code commit detected: ' + ', '.join(code))
            elif verb in ('add', 'stage'):
                if separator == '|' or any(os.path.basename(t) == 'xargs' for t in tokens): state['uncertain'] = 'piped git add'
                state['adds'].append((repo, args))
            elif verb in ('status', 'diff', 'log', 'show', 'ls-files', 'rev-parse', 'branch', 'fetch', 'remote', 'tag'): pass
            elif verb == 'config' and not any(x.startswith(('--unset', '--add', '--replace', '--remove', '--rename', '--edit')) for x in args) and (not args or any(x in args for x in ('--get', '--get-all', '--list', '-l', '--get-regexp', '--get-urlmatch')) or len([x for x in args if not x.startswith('-')]) == 1): pass
            elif verb == 'checkout' and any(x in args for x in ('-b', '-B')): pass
            elif verb == 'switch' and '--discard-changes' not in args and '-f' not in args: pass
            elif verb == 'stash' and args and args[0] in ('list', 'show'): pass
            else: state['uncertain'] = 'git ' + verb
        else:
            name = os.path.basename(tokens[0])
            if not safe_redirects(tokens): state['uncertain'] = 'redirection'
            elif name == 'sed' and ('-n' not in tokens[1:] or any(t.startswith('--in-place') or (t.startswith('-') and not t.startswith('--') and 'i' in t[1:]) for t in tokens[1:])): state['uncertain'] = 'sed'
            elif name == 'go' and len(tokens)>1 and tokens[1] in ('test', 'vet', 'list', 'version', 'env', 'doc'): pass
            elif name == 'gofmt' and any(t in ('-l', '-d') for t in tokens) and '-w' not in tokens: pass
            elif name == 'golangci-lint' and len(tokens)>1 and tokens[1] == 'run' and not any(t == '--fix' or t.startswith('--fix=') for t in tokens): pass
            elif name == 'make' and '-n' in tokens: pass
            elif name not in ('echo', 'printf', 'cat', 'true', 'test', '[', 'ls', 'pwd', 'grep', 'head', 'tail', 'sed', 'wc'): state['uncertain'] = name
        pipeline.extend(tokens + local_bodies)


def main():
    raw = sys.stdin.read()
    try:
        payload = json.loads(raw)
        if payload['tool_name'] != 'Bash': return
        command = payload['tool_input']['command']; cwd = payload['cwd']
        if not isinstance(command, str) or not isinstance(cwd, str): raise ValueError('invalid payload fields')
        inspect(command, cwd, {'adds': [], 'uncertain': None})
    except Exception as exc:
        if 'git' in raw and 'commit' in raw and (not isinstance(exc, json.JSONDecodeError) or not TICKET.search(raw)): fail('cannot parse or inspect this git commit: ' + str(exc))


if __name__ == '__main__': main()
