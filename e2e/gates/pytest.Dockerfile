# Test image for the gates' test repo: the repo's toolchain with its dependencies (pytest and coverage, which leave
# $A2DLC_COVERAGE for the coverage precondition). It runs without network.
# pytest 9.0.3 fixes PYSEC-2026-1845 (pytest 8.3.3 is the vulnerable pin); the same pin is in the gates-prop image.
FROM python:3.12-slim
RUN pip install --no-cache-dir pytest==9.0.3 coverage==7.6.1
