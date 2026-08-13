from __future__ import annotations

import ssl
from pathlib import Path


def server_ssl_context(
    certificate: Path | None, private_key: Path | None
) -> ssl.SSLContext | None:
    if certificate is None and private_key is None:
        return None
    if certificate is None or private_key is None:
        raise ValueError("both BRIA_TLS_CERT and BRIA_TLS_KEY are required")
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain(certificate, private_key)
    return context


def client_ssl_context(ca_file: Path | None) -> ssl.SSLContext | None:
    if ca_file is None:
        return None
    context = ssl.create_default_context(cafile=ca_file)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    return context
