# CordBrief

CordBrief is a tiny, self-hosted Discord daily-digest application written in Go with zero third-party dependencies.

## Overview

CordBrief reads messages from explicitly configured Discord channels over the documented Discord HTTP API, summarizes them using an OpenAI-compatible LLM endpoint, and posts a structured, link-attributed daily digest back to a designated channel.

- **Zero Third-Party Dependencies**: Built exclusively with Go standard library.
- **Privacy & Least Privilege**: Raw Discord messages are kept only in memory during digestion and are never persisted to disk. No database is required.
- **Fail-Safe Checkpointing**: Only operational metadata (`last_window_end`) is safely persisted after successful delivery.

## Specification

Refer to [SPEC.md](SPEC.md) for the complete v0.1 Build Contract and architecture.
