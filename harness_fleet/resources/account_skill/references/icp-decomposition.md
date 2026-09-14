# ICP Decomposition Guide

This guide teaches the model how to take any human outbound pitch and break it into high-precision technical search criteria.

---

## The 3-Signal Framework

When an operator says:
> *"I sell database performance monitoring to B2B SaaS companies."*
> *"We help fintech companies prevent payment latency."*
> *"Our tool automates SOC2 compliance for healthtech startups."*

Never search for broad terms like "fintech" or "B2B SaaS". Deconstruct the request into **Signals**:

### 1. Architecture Signals (What tools must they be running?)
Identify the underlying infrastructure that indicates a company is capable of using or needing the product:
- Primary databases: `PostgreSQL`, `MySQL`, `MongoDB`, `DynamoDB`, `ClickHouse`, `Cassandra`
- Streaming & Event brokers: `Apache Kafka`, `RabbitMQ`, `AWS SQS`, `Apache Flink`
- Cloud & Container: `Kubernetes`, `AWS EKS`, `GCP GKE`, `Terraform`, `Docker`
- AI / Vector: `Pinecone`, `Milvus`, `Qdrant`, `Weaviate`, `vLLM`, `Ollama`

### 2. Bottleneck & Pain Signals (What are they struggling with?)
Identify the language engineering teams use when experiencing pain:
- **Migration**: *"migrating off"*, *"replacing legacy"*, *"moving to v2"*, *"Kafka migration"*
- **Scale / Concurrency**: *"50k QPS"*, *"millions of daily events"*, *"high-throughput"*, *"multi-tenant"*
- **Latency / Performance**: *"query optimization"*, *"p99 latency"*, *"connection pooling"*, *"caching"*
- **Reliability / Outages**: *"zero-downtime"*, *"failover"*, *"disaster recovery"*, *"high-availability"*
- **Compliance / Governance**: *"SOC2 Type II"*, *"HIPAA compliance"*, *"PII masking"*, *"data residency"*

### 3. Hiring Urgency Signals (Who has the budget?)
Look for roles where the job description outlines the exact initiative:
- `Staff Infrastructure Engineer`
- `Senior Data Platform Engineer`
- `Principal Distributed Systems Engineer`
- `Lead Site Reliability Engineer (SRE)`
- `Security & Compliance Architect`

---

## Translation Examples

### Example A: "We sell ClickHouse query optimization"
- **Stack Keywords**: `ClickHouse`, `PostgreSQL`, `Kafka`, `Redshift`, `BigQuery`
- **Pain Keywords**: *"slow dashboards"*, *"query timeouts"*, *"high memory consumption"*, *"billion rows"*
- **Hiring Roles**: `Data Platform Engineer`, `Analytics Engineer`, `Infrastructure Engineer`
- **ATS Search Query**:
  `site:jobs.ashbyhq.com OR site:boards.greenhouse.io "ClickHouse" ("latency" OR "cluster" OR "optimize")`

### Example B: "We sell automated database migration tools"
- **Stack Keywords**: `PostgreSQL`, `MySQL`, `Kafka`, `Debezium`, `Change Data Capture (CDC)`
- **Pain Keywords**: *"zero-downtime migration"*, *"legacy billing"*, *"dual-write"*, *"backfill"*
- **Hiring Roles**: `Staff Backend Engineer`, `Platform Architect`
- **ATS Search Query**:
  `site:jobs.ashbyhq.com OR site:boards.greenhouse.io ("migration" OR "modernize") ("PostgreSQL" OR "Kafka")`

### Example C: "We sell real-time feature stores for AI teams"
- **Stack Keywords**: `PyTorch`, `Ray`, `Feast`, `Redis`, `Kafka`, `Snowflake`
- **Pain Keywords**: *"feature latency"*, *"real-time inference"*, *"model training pipeline"*, *"feature drift"*
- **Hiring Roles**: `Staff MLOps Engineer`, `Machine Learning Platform Engineer`
- **ATS Search Query**:
  `site:jobs.ashbyhq.com OR site:jobs.lever.co "MLOps" ("feature store" OR "real-time inference")`
