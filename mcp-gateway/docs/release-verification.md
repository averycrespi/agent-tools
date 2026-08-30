# Release verification and acceptance evidence

Audience: Maintainers producing release evidence

Purpose: Run and adopt exact-revision acceptance evidence.

This guide owns the purpose-based verification DAG, clean-revision acceptance, report adoption, native and external evidence classification, and failure discipline. The Makefile is authoritative for available target definitions; this guide explains how release owners compose them.

## Guide boundary

- See [maintainer guidance](../CLAUDE.md) for package ownership and editing invariants.
- See [DESIGN](../DESIGN.md) for normative security and compatibility boundaries.
- See [frontend development](frontend-development.md) for the separate live-reload and production-asset workflows.

Historical evidence is not reinterpreted or adopted as evidence for a different revision or definition set.
