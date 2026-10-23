#!/usr/bin/env python3
"""Explicitly invoked user-level launchd installer. No sudo, no scheduled installation."""
import argparse
import os
from pathlib import Path
import plistlib
import shutil
import subprocess

parser = argparse.ArgumentParser(description='Install or remove the Foldsnap login service')
parser.add_argument('--uninstall', action='store_true')
args = parser.parse_args()
home = Path.home()
label = 'app.localbackup.agent'
plist = home / 'Library/LaunchAgents' / f'{label}.plist'
domain = f'gui/{os.getuid()}'
if args.uninstall:
    subprocess.run(['launchctl', 'bootout', domain, str(plist)], check=False)
    plist.unlink(missing_ok=True)
    print('Removed background service. Backups and settings are preserved.')
else:
    source = Path(__file__).resolve().parents[2] / 'release/Foldsnap.app'
    if not source.exists():
        raise SystemExit('Run make package first.')
    target = home / 'Applications/Foldsnap.app'
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists():
        raise SystemExit(f'{target} already exists. Move it aside before installing a new build.')
    shutil.copytree(source, target)
    data = home / 'Library/Application Support/LocalBackup'
    data.mkdir(mode=0o700, parents=True, exist_ok=True)
    plist.parent.mkdir(parents=True, exist_ok=True)
    config = {'Label': label, 'ProgramArguments': [str(target / 'Contents/MacOS/localbackup-service'), '-open=false'],
              'RunAtLoad': True, 'KeepAlive': True, 'ThrottleInterval': 30,
              'StandardOutPath': str(data / 'service.log'), 'StandardErrorPath': str(data / 'service-error.log'),
              'Umask': 0o077, 'ProcessType': 'Background'}
    with plist.open('wb') as f:
        plistlib.dump(config, f)
    plist.chmod(0o600)
    subprocess.run(['launchctl', 'bootstrap', domain, str(plist)], check=True)
    print(f'Installed {target}. Open the app to access the UI.')
