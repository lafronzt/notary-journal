# New York notary journal requirements review

Reviewed **November 23, 2026**. Scope: an individual's New York notary journal, digitization of existing paper entries, local SQLite access, and protected third-party archive storage. This is an engineering review of published rules, not a legal opinion, certification, or review of a particular commission or notarization.

## Sources reviewed

1. [New York Department of State, Notary Public License Law, September 2026](https://dos.ny.gov/system/files/documents/2026/09/notary-public-license-law_09.2026.pdf). Primary agency publication containing Executive Law §§130, 135-c and 19 NYCRR Part 182, including §§182.8–182.10.
2. [19 NYCRR §182.9, Recordkeeping and Reporting](https://www.law.cornell.edu/regulations/new-york/19-NYCRR-182.9). Cornell reproduction; notes adoption effective January 25, 2023. Its recordkeeping text agrees with the September 2026 agency publication.
3. [NYS Department of State, Notary Public](https://dos.ny.gov/notary-public). Agency overview and licensing context.

The publication, rather than a search-result summary or proposed bill, supplies the governing text used here. Changes after review require reassessment.

## Requirements mapped to implementation

| Authority | Published requirement | Implemented support |
|---|---|---|
| §182.9(a) | All notaries must keep records sufficient to document compliance; records must be made contemporaneously with the act. | Separate act and recorded timestamps; a new-act attestation; paper imports explicitly labeled as historical transcriptions, with a date-only option when approximate time was not recorded. The app cannot verify the truth of an attestation. |
| §182.9(a), storage | Third-party storage must be safeguarded by a password or other secure authentication/access means. | Password-protected browser session; loopback access or HTTPS ingress; ADC-authenticated GCS, uniform bucket access, public access prevention; dedicated IAM role. Operator-managed disk/PVC encryption and OS/cluster access controls protect the SQL file. |
| §182.9(a)(1) | Date, approximate time, and type of acts. | Required timestamp with explicit UTC offset and service-type field. |
| §182.9(a)(2) | Names and addresses of any individuals for whom an act was performed. | Multiple required principal name/address rows. |
| §182.9(a)(3) | Number and type of services. | Positive integer service count and required type. Different service types use separate entries. |
| §182.9(a)(4) | Credential type; for witness verification under §182.5(b)(4)/(5), witness names and credential type if applicable. | Credential/personal-knowledge/witness identity basis; required credential type for the credential method; witness names and their credential or personal-knowledge basis. |
| §182.9(a)(5) | Verification procedures for personal appearance. | Required narrative describing each principal's identity and appearance verification. |
| §182.9(a)(6) | Electronic acts: communication technology and, where not included in that technology, certification authority and verification providers. | Required electronic-act metadata fields; integrated providers may be explicitly recorded as included in the named platform. |
| §182.9(b) | Records retained for at least ten years. | No app deletion feature; immutable SQL events; conservative retain-until field from later of entry or act; GCS bucket retention checked for ≥3660 days from upload. |
| §182.9(c) | Records must be producible to Secretary of State and others as necessary for obligations. | Search, full entry/history display, printed entry, original attachment downloads, and open-format full export/restore. Release/access decisions remain with the notary. |
| §182.8(b) | Electronic notary keeps required audio/video recording for ≥10 years from transaction; contents and PII restrictions apply. | Recording reference and separate-retention attestation. Small recordings can be attached. A reference alone is not preservation. This app does not capture or evaluate a recording. |
| Executive Law §135-c; §§182.3–182.8, 182.10 | Additional electronic notarization, registration, identity, signature, and communication requirements. | Explicit scope limit: journal/archive only, not an electronic notarization service or identity-proofing provider. |

## Design choices distinguished from legal mandates

- **SQLite and GCS are architectural choices.** §182.9 does not prescribe these products or a particular database format.
- **Append-only events, SHA-256 chaining, GCS conditional creation, versioning, and the 3660-day bucket policy are protective engineering choices.** §182.9 does not itself explicitly mandate a hash chain, WORM product, or bucket lock.
- **The journal signature, thumbprint, full ID number, document copy, and principal's credential image are not expressly required fields in §182.9.** This app avoids collecting these by default. Another applicable law, transaction requirement, or retention obligation may require more.
- **Retaining from the later of act or entry is conservative policy.** §182.9(b) says at least ten years; this implementation avoids relying on an earlier historical act to shorten retention of newly digitized records. §182.8(b) expressly measures electronic recording retention from the transaction date.
- **Paper import is evidence preservation.** A scan/transcription today does not cure a failure to create a required contemporaneous record. It is not a declaration that paper originals may be destroyed. Do not fabricate missing facts.
- **Corrections are additional events.** The original remains visible. The app checks completeness, not the truth or legal sufficiency of recorded procedures or the validity of the underlying act.

## Operational conditions and unresolved legal questions

The notary remains responsible for the journal, including after expiration of a commission, provider changes, or device loss. Maintain paid cloud service, authorized access, encryption-key availability where applicable, and the ability to export throughout retention. Validate backup recovery before depending on cloud-only disaster recovery.

An unlocked GCS bucket retention policy is enforceable against ordinary object deletions but can be shortened by a sufficiently privileged administrator. Dedicated runtime credentials cannot change it. Irreversible retention-policy locking should be reviewed separately once the bucket and recovery path have been tested.

This review does not establish that paper originals can be destroyed, approve the notary's evidence-of-identity procedure, settle disclosure requests, or provide a complete privacy/security legal analysis. Consider other duties such as New York data-security and breach requirements where the actual information collected brings them into scope. Full identification numbers and credential images are intentionally unnecessary for the default workflow.

There is no compliance certification or claim of Department of State approval. Before relying on this journal for regulated practice, check the current rules against the actual notarial workflow, including any electronic-notarization provider and recording-retention arrangement.
