#!/usr/bin/env python3
"""Configure the ui3344 subscription service with sane, offline defaults:

  * enable the raw, JSON and Clash subscription formats
  * embed a built-in routing ruleset (ad-block + China direct + proxy the rest)
    into the Clash/Mihomo profile, so clients route without any remote list.

Idempotent: only writes the keys it owns; safe to re-run after upgrades.
Run after the panel has created its database (i.e. after the service first
started); the subscription server reads these at start-up, so restart ui3344
afterwards for them to take effect.

Env overrides:
  UI3344_DB  sqlite path (default /etc/ui3344/ui3344.db)
Exit code is non-zero only when the database is missing.
"""
import os
import sqlite3
import sys

DB = os.environ.get("UI3344_DB", "/etc/ui3344/ui3344.db")

CLASH_RULES = "\n".join([
    # ads / telemetry -> block
    "DOMAIN-SUFFIX,doubleclick.net,REJECT",
    "DOMAIN-SUFFIX,googlesyndication.com,REJECT",
    "DOMAIN-SUFFIX,googleadservices.com,REJECT",
    "DOMAIN-SUFFIX,googletagmanager.com,REJECT",
    "DOMAIN-SUFFIX,google-analytics.com,REJECT",
    "DOMAIN-SUFFIX,ads.google.com,REJECT",
    "DOMAIN-SUFFIX,facebook.net,REJECT",
    "DOMAIN-KEYWORD,analytics,REJECT",
    "DOMAIN-KEYWORD,doubleclick,REJECT",
    "DOMAIN-KEYWORD,adservice,REJECT",
    # mainland China -> direct
    "DOMAIN-SUFFIX,cn,DIRECT",
    "DOMAIN-KEYWORD,-cn,DIRECT",
    "GEOIP,CN,DIRECT",
    # everything else falls through to the profile's own MATCH,PROXY
])

SETTINGS = {
    "subEnable": "true",
    "subJsonEnable": "true",
    "subClashEnable": "true",
    "subClashEnableRouting": "true",
    "subClashRules": CLASH_RULES,
    "subTitle": "雷电面板",
}


def main():
    if not os.path.exists(DB):
        print("configure-subscription: database not found: %s" % DB, file=sys.stderr)
        return 2
    conn = sqlite3.connect(DB)
    try:
        for key, value in SETTINGS.items():
            cur = conn.execute("update settings set value=? where key=?", (value, key))
            if cur.rowcount == 0:
                conn.execute("insert into settings(key,value) values(?,?)", (key, value))
        conn.commit()
    finally:
        conn.close()
    print("configure-subscription: applied %d settings" % len(SETTINGS))
    return 0


if __name__ == "__main__":
    sys.exit(main())
