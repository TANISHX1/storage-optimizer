# Intelligent Storage Optimizer — System Documentation

Welcome to the comprehensive technical documentation for the **Intelligent Storage Optimizer**. This documentation is designed both as an engineering reference manual and as an in-depth systems programming learning guide.

---

## Documentation Index

| Document | Description | Key Topics |
| :--- | :--- | :--- |
| [01. Architecture & Design](file:///home/blazex/Documents/git/storage-optimizer/docs/01-architecture-and-design.md) | High-level system architecture and component topology | Multi-tier architecture, Go core, Python layer, Wails GUI, Single Source of Truth |
| [02. Systems Programming & Linux Internals](file:///home/blazex/Documents/git/storage-optimizer/docs/02-systems-programming-and-linux.md) | OS kernel concepts, VFS, and low-level syscalls | `os.Lstat` vs `os.Stat`, `syscall.Stat_t`, Inodes, hardlinks, symlinks, `relatime`, streaming I/O |
| [03. Concurrency & Data Flow](file:///home/blazex/Documents/git/storage-optimizer/docs/03-concurrency-and-data-flow.md) | Goroutines, channels, and end-to-end data pipeline | Bounded worker pools, work discovery queue, backpressure, funnel-to-single-writer |
| [04. Database & Schema Contract](file:///home/blazex/Documents/git/storage-optimizer/docs/04-database-and-schema.md) | SQLite storage engine and schema specifications | SQLite WAL mode, PRAGMAs, batch transactions, `files`, `scan_snapshots`, `actions_log` |
| [05. Local HTTP API & Integration Contract](file:///home/blazex/Documents/git/storage-optimizer/docs/05-api-and-python-gui-contract.md) | Interface contract for Python & GUI layers | REST API endpoints, JSON request/response formats, Python forecasting integration |
| [06. Operations & CLI Reference](file:///home/blazex/Documents/git/storage-optimizer/docs/06-operations-and-cli.md) | CLI commands, compilation, and testing workflows | Build flags, CLI commands, verification scripts, benchmarking |

---

## High-Level Architecture Overview

```
                      ┌─────────────────────────────────┐
                      │    Desktop GUI Shell (Wails)    │
                      │    (HTML5 / CSS3 / Vanilla JS)  │
                      └────────────────┬────────────────┘
                                       │ HTTP / JSON
                                       ▼
 ┌───────────────────────────┐   ┌───────────────────────────┐
 │ Python Layer (Sahil D7)   │──►│ Go Systems Core (HTTP API)│
 │ Time-Series Forecast / ML │   │ Port: 127.0.0.1:8080      │
 └───────────────────────────┘   └─────────────┬─────────────┘
                                               │
                        ┌──────────────────────┴──────────────────────┐
                        ▼                                             ▼
          ┌───────────────────────────┐                 ┌───────────────────────────┐
          │ Concurrent FS Scanner     │                 │ Action & Deletion Engine  │
          │ • Bounded Worker Pool     │                 │ • Pre-action Sanity Check │
          │ • Inode / Stat Extraction │                 │ • Trash with Audit Log    │
          │ • Two-pass Deduplication  │                 │ • Irreversible Remove     │
          └─────────────┬─────────────┘                 └─────────────┬─────────────┘
                        │                                             │
                        │   ┌─────────────────────────────────────┐   │
                        └──►│ Funnel Channel (chan FileMetadata)  │◄──┘
                            └──────────────────┬──────────────────┘
                                               │
                                               ▼
                                  ┌─────────────────────────┐
                                  │ DB BatchWriter Goroutine│ (Single Writer)
                                  └────────────┬────────────┘
                                               │
                                               ▼
                                  ┌─────────────────────────┐
                                  │ SQLite DB (WAL Mode)    │
                                  │ data/optimizer.db       │
                                  └─────────────────────────┘
```
