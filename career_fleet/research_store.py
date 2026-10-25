"""Append-only Process projections; no scoring, identity or scheduling decisions."""
from __future__ import annotations
import hashlib
import json
from pathlib import Path
import sqlite3


class ResearchStore:
    def __init__(self, path: Path, *, readonly: bool = False):
        self.connection = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True) if readonly else sqlite3.connect(path)
        self.connection.row_factory = sqlite3.Row
        if readonly:
            return
        self.connection.executescript('''
        CREATE TABLE IF NOT EXISTS process_runs (
          sequence INTEGER PRIMARY KEY AUTOINCREMENT,
          event_id TEXT NOT NULL UNIQUE,
          response_json TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS process_leads (
          run_sequence INTEGER NOT NULL REFERENCES process_runs(sequence),
          lead_id TEXT NOT NULL,
          lead_json TEXT NOT NULL,
          PRIMARY KEY(run_sequence, lead_id)
        );
        CREATE TABLE IF NOT EXISTS process_attempts (
          sequence INTEGER PRIMARY KEY AUTOINCREMENT,
          event_id TEXT NOT NULL,
          response_digest TEXT NOT NULL,
          response_json TEXT NOT NULL,
          UNIQUE(event_id,response_digest)
        );
        CREATE TABLE IF NOT EXISTS process_settlements (
          event_id TEXT PRIMARY KEY,
          result_digest TEXT NOT NULL
        );
        CREATE TABLE IF NOT EXISTS source_blocks (
          block_digest TEXT PRIMARY KEY,
          block_name TEXT NOT NULL,
          candidate_json TEXT NOT NULL,
          event_id TEXT NOT NULL
        );
        ''')

    def record(self, response: dict) -> str:
        event_id = response['event_id']
        raw = json.dumps(response, ensure_ascii=False, sort_keys=True)
        response_digest = hashlib.sha256(raw.encode()).hexdigest()
        result = response.get('outputs', {}).get('result')
        with self.connection:
            self.connection.execute('INSERT OR IGNORE INTO process_attempts(event_id,response_digest,response_json) VALUES (?,?,?)', (event_id,response_digest,raw))
            prior = self.connection.execute('SELECT sequence,response_json FROM process_runs WHERE event_id=?', (event_id,)).fetchone()
            if prior is None:
                sequence = self.connection.execute('INSERT INTO process_runs(event_id,response_json) VALUES (?,?)', (event_id,raw)).lastrowid
            else:
                sequence = prior['sequence']
            if result is not None:
                digest = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
                existing = self.connection.execute('SELECT result_digest FROM process_settlements WHERE event_id=?', (event_id,)).fetchone()
                if existing and existing['result_digest'] != digest:
                    raise ValueError('settled Process result changed for the same event')
                self.connection.execute('INSERT OR IGNORE INTO process_settlements VALUES (?,?)', (event_id,digest))
                for lead in result.get('candidates', []):
                    self.connection.execute('INSERT OR IGNORE INTO process_leads VALUES (?,?,?)', (sequence,lead['id'],json.dumps(lead,ensure_ascii=False)))
                for candidate in result.get('source_candidates', []):
                    block = candidate.get('block') or {}
                    if candidate.get('status') == 'verified' and block.get('digest'):
                        self.connection.execute('INSERT OR IGNORE INTO source_blocks VALUES (?,?,?,?)', (block['digest'],block['name'],json.dumps(candidate,ensure_ascii=False),event_id))
        return event_id

    def source_health(self) -> list[dict]:
        rows = self.connection.execute("""SELECT health.value AS health_json, attempts.event_id
          FROM process_attempts attempts, json_each(attempts.response_json, '$.outputs.result.source_health') health
          ORDER BY attempts.sequence""")
        latest = {}
        for row in rows:
            health = json.loads(row['health_json'])
            if 'source_digest' not in health:
                continue
            key = (health['source_id'], health['source_digest'], health['url'])
            latest[key] = {**health, 'event_id': row['event_id']}
        return list(latest.values())

    def source_candidates(self) -> list[dict]:
        rows = self.connection.execute("""SELECT candidate.value AS candidate_json, attempts.event_id
          FROM process_attempts attempts, json_each(attempts.response_json, '$.outputs.result.source_candidates') candidate
          ORDER BY attempts.sequence""")
        latest = {}
        for row in rows:
            candidate = json.loads(row['candidate_json'])
            latest[candidate['id']] = {**candidate, 'event_id': row['event_id']}
        return list(latest.values())

    def source_candidate(self, identifier: str) -> dict:
        for candidate in self.source_candidates():
            if candidate['id'] == identifier:
                return candidate
        raise ValueError(f'unknown source candidate: {identifier}')

    def blocks(self) -> list[dict]:
        rows = self.connection.execute('SELECT candidate_json,event_id FROM source_blocks ORDER BY block_name,block_digest')
        return [{**json.loads(row['candidate_json']), 'event_id': row['event_id']} for row in rows]

    def leads(self, event_id: str | None = None) -> list[dict]:
        if event_id:
            rows = self.connection.execute('SELECT lead_json FROM process_leads JOIN process_runs ON sequence=run_sequence WHERE event_id=? ORDER BY lead_id', (event_id,))
        else:
            rows = self.connection.execute('''SELECT lead_json FROM process_leads p
            WHERE run_sequence=(SELECT MAX(run_sequence) FROM process_leads WHERE lead_id=p.lead_id)
            ORDER BY run_sequence,lead_id''')
        return [json.loads(row[0]) for row in rows]

    def dossier(self, lead_id: str) -> list[dict]:
        rows = self.connection.execute('''SELECT event_id,lead_json FROM process_leads
        JOIN process_runs ON sequence=run_sequence WHERE lead_id=? ORDER BY sequence''', (lead_id,))
        return [{'event_id':row[0],'candidate':json.loads(row[1])} for row in rows]

    def run(self, event_id: str) -> dict:
        row = self.connection.execute('SELECT response_json FROM process_attempts WHERE event_id=? ORDER BY sequence DESC LIMIT 1', (event_id,)).fetchone()
        if row is None:
            row = self.connection.execute('SELECT response_json FROM process_runs WHERE event_id=?', (event_id,)).fetchone()
        if row is None:
            raise ValueError(f'unknown execution: {event_id}')
        return json.loads(row[0])

    def close(self):
        self.connection.close()
