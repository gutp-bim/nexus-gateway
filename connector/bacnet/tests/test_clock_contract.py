# Copyright 2026 nexus-gateway contributors
# SPDX-License-Identifier: Apache-2.0

"""Pinned RI oracle acceptance cases; no RI service or trust mechanism is run."""
import copy
import hashlib
import json

import pytest

from .clock_contract_support import CONTRACT, ROOT, oracle, read_fixture


SUITE = read_fixture("time-contract-cases.json")


def test_upstream_files_are_pinned_and_static_snapshot_unchanged():
    manifest = json.loads((CONTRACT / "UPSTREAM.json").read_text())
    assert manifest["commit"] == "9b643391d7273de4d92166d2474de1e5e0ea9f83"
    for name, digest in manifest["files"].items():
        assert hashlib.sha256((CONTRACT / name).read_bytes()).hexdigest() == digest, name
    assert (CONTRACT / "fixtures/provisioning.json").read_bytes() == (
        ROOT / "internal/provisioning/testdata/clock-provisioning.json"
    ).read_bytes()


@pytest.mark.parametrize("case", SUITE["cases"], ids=lambda c: c["name"])
def test_ri_contract_cases(case):
    contract = oracle()
    snapshot = read_fixture("provisioning.json")
    frame = read_fixture(case.get("frame", "telemetry.json"))
    frame.update(case.get("framePatch", {}))
    frame["attributes"].update(case.get("attributes", {}))
    for key in case.get("omitAttributes", []):
        frame["attributes"].pop(key, None)
    acquisition = read_fixture(SUITE["acquisitionEvidence"])
    acquisition.update(case.get("acquisitionPatch", {}))
    context = read_fixture(case["context"]) if "context" in case else None
    receiver = read_fixture(SUITE["receiverEvidence"]) if context is not None else None
    if context is not None:
        context.update(case.get("contextPatch", {}))
        receiver.update(case.get("receiverPatch", {}))
        if "committedContextPatch" in case:
            receiver["committedContext"].update(case["committedContextPatch"])
    original = copy.deepcopy((snapshot, frame, acquisition, context, receiver))
    profile = case.get("profile", SUITE["profile"])
    if "expectedError" in case:
        with pytest.raises(contract.ContractError) as error:
            contract.validate_observation(snapshot, frame, acquisition, context, receiver, profile)
        assert str(error.value) == case["expectedError"]
    else:
        result = contract.validate_observation(snapshot, frame, acquisition, context, receiver, profile)
        assert all(result[k] == v for k, v in case["expected"].items())
        assert result == contract.validate_observation(snapshot, frame, acquisition, context, receiver, profile)
    assert original == (snapshot, frame, acquisition, context, receiver)
