# Tests image of VARA-2: the instance's SWE-bench image + coverage (+ pytest where missing: django does not use it,
# sympy does) + the tests command and the django log converter. The network is used only at build time.
ARG BASE
FROM $BASE
RUN /opt/miniconda3/envs/testbed/bin/pip install --no-cache-dir -q coverage==7.6.1 && \
    (/opt/miniconda3/envs/testbed/bin/python -c "import pytest" 2>/dev/null || /opt/miniconda3/envs/testbed/bin/pip install --no-cache-dir -q pytest)
COPY run_tests.sh log2junit_django.py /a2dlc/
