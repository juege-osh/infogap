#!/usr/bin/env python3
"""Create a consistent private SQLite backup; compatible with Python 3.6."""
import argparse
import datetime
import os
import sqlite3
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--db', required=True)
parser.add_argument('--directory', required=True)
args = parser.parse_args()
os.umask(0o077)
os.makedirs(args.directory, exist_ok=True)
stamp = datetime.datetime.utcnow().strftime('%Y%m%dT%H%M%SZ')
output = os.path.join(args.directory, 'infogap-' + stamp + '.sqlite')
source = sqlite3.connect(args.db)
if hasattr(source, 'backup'):
    target = sqlite3.connect(output)
    source.backup(target)
    target.close()
else:
    # sqlite3's backup API was added to Python in 3.7; SQL dump is a consistent
    # read transaction and safely recreates this small v1 database on 3.6.
    source.execute('BEGIN')
    target = sqlite3.connect(output)
    target.executescript('\n'.join(source.iterdump()))
    target.close()
    source.rollback()
source.close()
check = sqlite3.connect(output)
assert check.execute('PRAGMA integrity_check').fetchone()[0] == 'ok'
check.close()
os.chmod(output, 0o600)
print('Verified backup:', output)
