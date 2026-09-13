# Security Policy

## Supported Versions

Only the [latest release](https://github.com/katbyte/go-kt/releases/latest) is supported — please update before reporting an issue.

## Reporting a Vulnerability

Please **do not** open a public issue for security vulnerabilities.

Instead, report privately via [GitHub's private vulnerability reporting](https://github.com/katbyte/go-kt/security/advisories/new).

I will do my best to acknowledge reports within 2 weeks and aim to release a fix or mitigation within 6 weeks for confirmed issues; timelines are best-effort.

## Scope

`go-kt` is a library shared by katbyte's command-line tools. `chttp` trace-logs full HTTP requests and responses (including `Authorization` headers) at TRACE level, so anything that causes that output to appear at a lower level, or a retry that re-sends a mutation it should not, is particularly relevant.
