# Workflow

1. Build once with `career-fleet build`.
2. Create a request with `career-fleet init --objective "the requested objective" --output request.json`.
3. Fill its selected sources, vocabulary and questions. Use the bundled request schema; unknown inputs refuse. Enable acquisition and any request allowance only under existing user authority. There is no product-wide source quota.
4. Execute `career-fleet discover --request request.json --db career_fleet.db`.
5. Inspect `career-fleet list --db career_fleet.db` and `career-fleet dossier --id ID --db career_fleet.db`.
6. Export every retained lead with `career-fleet export --db career_fleet.db --output results.json`, or include `--run EVENT_ID` for a complete exact execution.

A question with acquisition `source_document` can verify a sparse unidentified lead. An exact document subject binding is needed before its content becomes attributed company facts. Exact field/scope conditions distinguish support, contradiction, unknown and conflict. Unrequested personal fit stays unassessed.

Raw captures, matches, assertions, action dispositions and gaps are returned together. Private run artifacts include the execution journal and its key. Never publish them or a personal research database.
