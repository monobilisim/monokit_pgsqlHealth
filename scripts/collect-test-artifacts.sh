#!/bin/sh
if [ -d /artifacts ]; then
    echo "Extracting artifacts to /artifacts..."

    suffix=""
    if [ -n "$TEST_SUITE" ]; then
        suffix="-$TEST_SUITE"
    fi

    cp /var/lib/monokit2/monokit2.db "/artifacts/monokit2$suffix.db" 2>/dev/null
    cp /var/log/monokit2.log "/artifacts/monokit2$suffix.log" 2>/dev/null

    if [ -n "$HOST_UID" ] && [ -n "$HOST_GID" ]; then
        chown "$HOST_UID:$HOST_GID" /artifacts/* 2>/dev/null
    fi
else
    echo "/artifacts directory not found, skipping artifact extraction."
fi
