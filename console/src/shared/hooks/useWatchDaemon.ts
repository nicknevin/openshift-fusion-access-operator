import { type WatchK8sResource } from "@openshift-console/dynamic-plugin-sdk";
import { useNormalizedK8sWatchResource } from "@/shared/utils/console/UseK8sWatchResource";
import type { Daemon } from "@/shared/types/ibm-spectrum-scale/Daemon";

export const useWatchDaemon = (
  options: Omit<
    WatchK8sResource,
    "groupVersionKind" | "namespaced" | "namespace" | "isList"
  > = {}
) =>
  useNormalizedK8sWatchResource<Daemon>({
    ...options,
    isList: true,
    namespaced: true,
    namespace: "ibm-spectrum-scale",
    groupVersionKind: {
      group: "scale.spectrum.ibm.com",
      version: "v1beta1",
      kind: "Daemon",
    },
  });
