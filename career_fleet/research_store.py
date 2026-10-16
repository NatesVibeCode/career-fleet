"""Append-only projections of printed Process results; no scoring or identity decisions."""
from __future__ import annotations
import json
from pathlib import Path
import sqlite3


class ResearchStore:
    def __init__(self, path: Path):
        self.connection = sqlite3.connect(path)
        self.connection.row_factory = sqlite3.Row
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
        ''')

    def record(self, response: dict) -> str:
        event_id = response['event_id']
        with self.connection:
            sequence = self.connection.execute('INSERT INTO process_runs(event_id,response_json) VALUES (?,?)', (event_id,json.dumps(response,ensure_ascii=False))).lastrowid
            for lead in response.get('outputs',{}).get('result',{}).get('candidates',[]):
                self.connection.execute('INSERT INTO process_leads VALUES (?,?,?)', (sequence,lead['id'],json.dumps(lead,ensure_ascii=False)))
        return event_id

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
        row=self.connection.execute('SELECT response_json FROM process_runs WHERE event_id=?',(event_id,)).fetchone()
        if row is None:
            raise ValueError(f'unknown execution: {event_id}')
        return json.loads(row[0])

    def close(self):
        self.connection.close()
