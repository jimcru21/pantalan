# Pantalan

A self-hosted platform that deploys apps from git as containers on servers the operator owns.

## Language

### Infrastructure

**Node**:
A server running Docker that Pantalan manages. Today there is exactly one: the host Pantalan is installed on.
_Avoid_: Host, server, machine, Swarm node

### Access

**Admin**:
The single person account that signs in to the Dashboard with a username and password, created during installation.
_Avoid_: User, root, owner

**Admin Token**:
The shared secret that authenticates automation against the API, separate from the Admin's password.
_Avoid_: API key, password

**Dashboard**:
The browser interface to Pantalan, reached after the Admin signs in.
_Avoid_: Panel, console, UI
