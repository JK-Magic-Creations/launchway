#!/bin/sh
# Waits, for up to a minute, until something accepts connections on
# localhost:5432. Services start with the run and take a few seconds; the
# image does not need a database client for this.
set -e
i=0
until python3 -c "import socket; socket.create_connection(('127.0.0.1', 5432), 1).close()" 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 60 ]; then
    echo "nothing is listening on localhost:5432 after a minute" >&2
    exit 1
  fi
  sleep 1
done
echo "the database accepts connections"
