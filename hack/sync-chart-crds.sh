#!/usr/bin/env bash
# Copy the controller-gen CRDs into the Helm chart, wrapping each one in the
# chart's crd.enabled / crd.keep guards.
#
# The chart used to hold hand-copied CRDs, which silently went stale whenever
# api/v1alpha1 changed. Regenerating them from the single generated source is
# the only way the two stay in agreement.
#
# The chart lives in its own repository (devops-ia/helm-dwpk), so the
# destination has to be given rather than assumed:
#
#   ./hack/sync-chart-crds.sh ../helm-dwpk/charts/dwpk
#   make sync-chart-crds CHART_DIR=../helm-dwpk/charts/dwpk
#
# Nothing is written without it. This used to default to an in-repo path that
# stopped existing when the chart moved out, so every run wrote a stray tree
# here and the real chart went unsynced - the exact drift the paragraph above
# says this script exists to prevent.
set -euo pipefail

chart_dir="${1:-${CHART_DIR:-}}"
if [ -z "${chart_dir}" ]; then
  echo "sync-chart-crds: no chart directory given, nothing to sync." >&2
  echo "  usage: $0 <path-to-chart>    (e.g. ../helm-dwpk/charts/dwpk)" >&2
  exit 0
fi
if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "sync-chart-crds: ${chart_dir} is not a Helm chart (no Chart.yaml)." >&2
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_dir="$(cd "${chart_dir}" && pwd)"
src_dir="${repo_root}/config/crd/bases"
dest_dir="${chart_dir}/templates/crd"

mkdir -p "${dest_dir}"

for src in "${src_dir}"/*.yaml; do
  # config/crd/bases names files <group>_<plural>.yaml; the chart names them
  # <plural>.<group>.yaml, matching the CRD's own metadata.name.
  base="$(basename "${src}" .yaml)"
  group="${base%%_*}"
  plural="${base#*_}"
  dest="${dest_dir}/${plural}.${group}.yaml"

  {
    echo '{{- if .Values.crd.enabled }}'
    # Drop the leading YAML document separator, then splice the resource-policy
    # annotation in under the annotations key controller-gen already emits.
    tail -n +2 "${src}" | awk '
      /^  annotations:$/ {
        print
        print "    {{- if .Values.crd.keep }}"
        print "    \"helm.sh/resource-policy\": keep"
        print "    {{- end }}"
        next
      }
      { print }
    '
    echo '{{- end }}'
  } >"${dest}"

  echo "wrote ${dest#"${chart_dir}/"}"
done

# The manager ClusterRole drifts the same way the CRDs did: controller-gen
# rewrites config/rbac/role.yaml from the +kubebuilder:rbac markers, and the
# chart used to carry a hand-copied set of rules that nothing kept current.
manager_role="${chart_dir}/templates/rbac/manager-role.yaml"
mkdir -p "$(dirname "${manager_role}")"
{
  cat <<'HEADER'
apiVersion: rbac.authorization.k8s.io/v1
{{- if .Values.rbac.namespaced }}
kind: Role
{{- else }}
kind: ClusterRole
{{- end }}
metadata:
{{- if .Values.rbac.namespaced }}
  namespace: {{ .Release.Namespace }}
{{- end }}
  name: {{ include "dwpk.resourceName" (dict "suffix" "manager-role" "context" $) }}
HEADER
  sed -n '/^rules:/,$p' "${repo_root}/config/rbac/role.yaml"
} >"${manager_role}"

echo "wrote ${manager_role#"${chart_dir}/"}"
