# Pantalan

A self-hosted platform that deploys apps from git as containers on servers the operator owns.

## Language

### Infrastructure

**Node**:
A server running Docker that Pantalan manages. Today there is exactly one: the host Pantalan is installed on.
_Avoid_: Host, server, machine, Swarm node

### Apps

**App**:
Something the Admin deploys from one git repository and branch, served at one domain.
_Avoid_: Service, project, application

**Deployment**:
One attempt to put a specific commit of an App live: queued, then building, starting and live, or ending failed or superseded. Rolling back is a new Deployment of an earlier Deployment's commit.
_Avoid_: Release, build, rollout

**Deployment Log**:
A Deployment's own output, from fetching the commit until it is live or has failed.
_Avoid_: Build log

**App Log**:
What the App's live container writes to stdout and stderr.
_Avoid_: Container log, runtime log

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
