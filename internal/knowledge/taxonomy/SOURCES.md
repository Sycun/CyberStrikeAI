# Offline taxonomy sources

The JSON files contain 1,913 classification records. The ARTEX
`baarai/main` snapshot at `e5c7bfcbdd893db26c08f0b004e0169b4ab6ca85`
contributes 1,795 records. Its source manifest dates the snapshot to 2026-11-24.
The published OWASP v4.2 guide contributes the additional WSTG records. New
entries contain identifiers, names and classification metadata. Existing local
labels and summaries are preserved for matching unversioned IDs.

- `wstg.json` contains 133 [OWASP WSTG checklist](https://github.com/OWASP/wstg/blob/master/checklists/checklist.json) identifiers, 97 version-qualified [v4.2 identifiers](https://wstg.owasp.org/v4.2/) and 21 legacy unversioned identifiers absent from the snapshot. `WSTG-v42-INPV-13` identifies Format String Injection, following the published v4.2 contents. OWASP Foundation copyright. The adapted pack is distributed under [CC BY-SA 4.0](licenses/WSTG-LICENSE.txt).
- `cwe.json` contains 956 [MITRE CWE research-view weaknesses](https://cwe.mitre.org/data/downloads). Copyright © 2006–2026 The MITRE Corporation. This work is reproduced and distributed with the permission of The MITRE Corporation. [License notice](licenses/CWE-TERMS.txt) · [CWE Terms of Use](https://cwe.mitre.org/about/termsofuse.html)
- `attack.json` contains 706 [MITRE ATT&CK® Enterprise](https://attack.mitre.org/techniques/enterprise/) techniques and sub-techniques. © 2026 The MITRE Corporation. This work is reproduced and distributed with the permission of The MITRE Corporation. [License](licenses/ATTACK-LICENSE.txt) · [MITRE ATT&CK® Terms of Use](https://attack.mitre.org/resources/terms-of-use/)
