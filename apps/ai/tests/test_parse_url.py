# SPDX-License-Identifier: AGPL-3.0-or-later
from __future__ import annotations

from anomaly.cli import _parse_url


def test_parse_url_defaults_to_the_http_port_clickhouse_connect_speaks():
    # clickhouse-connect only talks to the HTTP interface. The native
    # protocol port (9000) answers with something it cannot parse.
    parsed = _parse_url("clickhouse://bamboo:pw@clickhouse/bamboo")
    assert parsed["port"] == 8123
    assert parsed["secure"] is False


def test_parse_url_accepts_the_controllers_http_dsn_unchanged():
    parsed = _parse_url("http://bamboo:pw@clickhouse:8123/bamboo")
    assert parsed == {
        "host": "clickhouse",
        "port": 8123,
        "username": "bamboo",
        "password": "pw",
        "database": "bamboo",
        "secure": False,
    }


def test_parse_url_https_is_secure_on_8443():
    parsed = _parse_url("https://u:p@ch.example.com/db")
    assert parsed["port"] == 8443
    assert parsed["secure"] is True
