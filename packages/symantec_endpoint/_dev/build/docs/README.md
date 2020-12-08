# Symantec Integration

This integration is for Symantec FortiOS and FortiClient Endpoint logs sent in the syslog format. It includes the following datasets for receiving logs:

- `firewall` dataset: consists of Symantec FortiGate logs.
- `clientendpoint` dataset: supports Symantec FortiClient Endpoint Security logs.
- `fortimail` dataset: supports Symantec FortiMail logs.
- `fortimanager` dataset: supports Symantec Manager/Analyzer logs.

## Compatibility

This integration has been tested against FortiOS version 6.0.x and 6.2.x. Versions above this are expected to work but have not been tested.

## Logs

### Firewall

Contains log entries from Symantec FortiGate applicances.

{{fields "firewall"}}

### Clientendpoint

The `clientendpoint` dataset collects Symantec FortiClient Endpoint Security logs.

{{fields "clientendpoint"}}

### Fortimail

The `fortimail` dataset collects Symantec FortiMail logs.

{{fields "fortimail"}}

### Fortimanager

The `fortimanager` dataset collects Symantec Manager/Analyzer logs.

{{fields "fortimanager"}}
