/**
 * TypeScript types for IBM Spectrum Scale Daemons API
 * Generated from OpenAPI schema: scale.spectrum.ibm.com/v1beta1/daemons-schema.json
 */

// Base Kubernetes types
export interface ObjectMeta {
  [key: string]: any;
}

// Enums
export type CloudEnv = "general";
export type IgnoreReplicaSpaceOnStat = "yes" | "no";
export type IgnoreReplicationForQuota = "yes" | "no";
export type IgnoreReplicationOnStatfs = "yes" | "no";
export type ProactiveReconnect = "yes" | "no";
export type ReadReplicaPolicy = "default" | "local" | "fastest";
export type TraceGenSubDir = "/var/mmfs/tmp/traces";
export type TscCmdAllowRemoteConnections = "yes" | "no";
export type VerbsRdma = "enable" | "disable";
export type VerbsRdmaCm = "enable" | "disable";
export type VerbsRdmaSend = "yes" | "no";
export type Edition = "data-access" | "data-management" | "erasure-code";
export type RoleName = "afm" | "storage" | "client";
export type ConditionStatus = "True" | "False" | "Unknown";

// Cluster Profile Configuration
export interface ClusterProfile {
  afmAsyncDelay?: string;
  afmDIO?: string;
  afmHashVersion?: string;
  afmMaxParallelRecoveries?: string;
  afmObjKeyExpiration?: string;
  backgroundSpaceReclaimThreshold?: string;
  cloudEnv?: CloudEnv;
  controlSetxattrImmutableSELinux?: string;
  encryptionKeyCacheExpiration?: string;
  enforceFilesetQuotaOnRoot?: string;
  ignorePrefetchLUNCount?: string;
  ignoreReplicaSpaceOnStat?: IgnoreReplicaSpaceOnStat;
  ignoreReplicationForQuota?: IgnoreReplicationForQuota;
  ignoreReplicationOnStatfs?: IgnoreReplicationOnStatfs;
  initPrefetchBuffers?: string;
  maxBufferDescs?: string;
  maxMBpS?: string;
  maxTcpConnsPerNodeConn?: string;
  maxblocksize?: string;
  nsdMaxWorkerThreads?: string;
  nsdMinWorkerThreads?: string;
  nsdMultiQueue?: string;
  nsdRAIDBlockDeviceMaxSectorsKB?: string;
  nsdRAIDBlockDeviceNrRequests?: string;
  nsdRAIDBlockDeviceQueueDepth?: string;
  nsdRAIDBlockDeviceScheduler?: string;
  nsdRAIDBufferPoolSizePct?: string;
  nsdRAIDDefaultGeneratedFD?: string;
  nsdRAIDDiskCheckVWCE?: string;
  nsdRAIDEventLogToConsole?: string;
  nsdRAIDFlusherFWLogHighWatermarkMB?: string;
  nsdRAIDMasterBufferPoolSize?: string;
  nsdRAIDMaxPdiskQueueDepth?: string;
  nsdRAIDMaxRecoveryRetries?: string;
  nsdRAIDMaxTransientStale2FT?: string;
  nsdRAIDMaxTransientStale3FT?: string;
  nsdRAIDNonStealableBufPct?: string;
  nsdRAIDReadRGDescriptorTimeout?: string;
  nsdRAIDReconstructAggressiveness?: string;
  nsdRAIDSmallThreadRatio?: string;
  nsdRAIDThreadsPerQueue?: string;
  nsdRAIDTracks?: string;
  nsdSmallThreadRatio?: string;
  nspdBufferMemPerQueue?: string;
  nspdQueues?: string;
  nspdThreadsPerQueue?: string;
  numaMemoryInterleave?: string;
  pagepoolMaxPhysMemPct?: string;
  panicOnIOHang?: string;
  pitWorkerThreadsPerNode?: string;
  prefetchPct?: string;
  prefetchThreads?: string;
  prefetchTimeout?: string;
  proactiveReconnect?: ProactiveReconnect;
  qMaxBlockShare?: string;
  qRevokeDisable?: string;
  readReplicaPolicy?: ReadReplicaPolicy;
  seqDiscardThreshold?: string;
  traceGenSubDir?: TraceGenSubDir;
  tscCmdAllowRemoteConnections?: TscCmdAllowRemoteConnections;
  tscCmdPortRange?: string;
  verbsPorts?: string;
  verbsRdma?: VerbsRdma;
  verbsRdmaCm?: VerbsRdmaCm;
  verbsRdmaSend?: VerbsRdmaSend;
}

// Host Aliases
export interface HostAlias {
  hostname: string;
  ip: string;
}

// Images
export interface Images {
  core?: string;
  coreInit?: string;
}

// Node Selector
export interface NodeSelector {
  [key: string]: string;
}

// Node Selector Expression
export interface NodeSelectorExpression {
  key: string;
  operator: string;
  values?: string[];
}

// NSD Device Path
export interface NsdDevicePath {
  devicePath?: string;
  deviceType?: string;
}

// NSD Devices Config
export interface NsdDevicesConfig {
  bypassDiscovery?: boolean;
  localDevicePaths?: NsdDevicePath[];
}

// Resource Requirements
export interface ResourceRequirements {
  cpu?: string;
  memory?: string;
}

// Role Profile Configuration
export interface RoleProfile {
  afmMaxParallelRecoveries?: string;
  backgroundSpaceReclaimThreshold?: string;
  controlSetxattrImmutableSELinux?: string;
  ignorePrefetchLUNCount?: string;
  initPrefetchBuffers?: string;
  maxBufferDescs?: string;
  maxMBpS?: string;
  maxTcpConnsPerNodeConn?: string;
  maxblocksize?: string;
  nsdMaxWorkerThreads?: string;
  nsdMinWorkerThreads?: string;
  nsdMultiQueue?: string;
  nsdRAIDBlockDeviceMaxSectorsKB?: string;
  nsdRAIDBlockDeviceNrRequests?: string;
  nsdRAIDBlockDeviceQueueDepth?: string;
  nsdRAIDBlockDeviceScheduler?: string;
  nsdRAIDBufferPoolSizePct?: string;
  nsdRAIDDefaultGeneratedFD?: string;
  nsdRAIDDiskCheckVWCE?: string;
  nsdRAIDEventLogToConsole?: string;
  nsdRAIDFlusherFWLogHighWatermarkMB?: string;
  nsdRAIDMasterBufferPoolSize?: string;
  nsdRAIDMaxPdiskQueueDepth?: string;
  nsdRAIDMaxRecoveryRetries?: string;
  nsdRAIDMaxTransientStale2FT?: string;
  nsdRAIDMaxTransientStale3FT?: string;
  nsdRAIDNonStealableBufPct?: string;
  nsdRAIDReadRGDescriptorTimeout?: string;
  nsdRAIDReconstructAggressiveness?: string;
  nsdRAIDSmallThreadRatio?: string;
  nsdRAIDThreadsPerQueue?: string;
  nsdRAIDTracks?: string;
  nsdSmallThreadRatio?: string;
  nspdBufferMemPerQueue?: string;
  nspdQueues?: string;
  nspdThreadsPerQueue?: string;
  numaMemoryInterleave?: string;
  pagepoolMaxPhysMemPct?: string;
  panicOnIOHang?: string;
  pitWorkerThreadsPerNode?: string;
  prefetchPct?: string;
  prefetchThreads?: string;
  prefetchTimeout?: string;
  proactiveReconnect?: ProactiveReconnect;
  seqDiscardThreshold?: string;
  tscCmdPortRange?: string;
  verbsPorts?: string;
  verbsRdma?: VerbsRdma;
  verbsRdmaCm?: VerbsRdmaCm;
  verbsRdmaSend?: VerbsRdmaSend;
}

// Role Configuration
export interface Role {
  limits?: ResourceRequirements;
  name: RoleName;
  profile?: RoleProfile;
  resources?: ResourceRequirements;
}

// Site Configuration
export interface Site {
  name: string;
  zone: string;
}

// Toleration
export interface Toleration {
  effect?: string;
  key?: string;
  operator?: string;
  tolerationSeconds?: number;
  value?: string;
}

// Label Selector
export interface LabelSelector {
  matchExpressions?: NodeSelectorExpression[];
  matchLabels?: { [key: string]: string };
}

// Update Pool
export interface UpdatePool {
  maxUnavailable?: number | string;
  name: string;
  nodeSelector?: LabelSelector;
  paused?: boolean;
}

// Update Configuration
export interface UpdateConfig {
  maxUnavailable?: number | string;
  paused?: boolean;
  pools?: UpdatePool[];
}

// Daemon Spec
export interface DaemonSpec {
  clusterNameOverride?: string;
  clusterProfile?: ClusterProfile;
  edition: Edition;
  hostAliases?: HostAlias[];
  images?: Images;
  nodeSelector?: NodeSelector;
  nodeSelectorExpressions?: NodeSelectorExpression[];
  nsdDevicesConfig?: NsdDevicesConfig;
  regionalDR?: any;
  roles?: Role[];
  site: Site;
  tolerations?: Toleration[];
  update?: UpdateConfig;
}

// Condition
export interface Condition {
  lastTransitionTime: string;
  message: string;
  observedGeneration: number;
  reason: string;
  status: ConditionStatus;
  type: string;
}

// Node Draining
export interface NodeDraining {
  node: string;
  ongoingPodEvictions: string[];
  podEvictionsFailed: string[];
}

// Pod Eviction Request
export interface PodEvictionRequest {
  pods: string;
  requestor: string;
}

// Cordon and Drain
export interface CordonAndDrain {
  nodesCordonedByOperator: string;
  nodesCordonedByOthers: string;
  nodesDraining?: NodeDraining[];
  podEvictionRequests?: PodEvictionRequest[];
}

// Pods Status
export interface PodsStatus {
  running: string;
  starting: string;
  terminating: string;
  unknown: string;
  waitingForDelete: string;
}

// Pods
export interface Pods {
  desired: string;
  total: string;
}

// Quorum Pods
export interface QuorumPods {
  running: string;
  total: string;
}

// Role Status
export interface RoleStatus {
  name: string;
  nodeCount: string;
  nodes: string;
  podCount: string;
  pods: string;
  runningCount: string;
}

// Pods Waiting to be Deleted
export interface PodsWaitingToBeDeleted {
  deleteReason: string;
  pods: string;
}

// Status Details
export interface StatusDetails {
  nodesRebooting: string;
  nodesUnreachable: string;
  podsStarting: string;
  podsTerminating: string;
  podsUnknown: string;
  podsWaitingToBeDeleted?: PodsWaitingToBeDeleted[];
  quorumPods: string;
}

// Tiebreaker
export interface Tiebreaker {
  version?: string;
}

// Update Pool Status
export interface UpdatePoolStatus {
  name: string;
  nodeCount: number;
  nodes: string;
}

// Update Status
export interface UpdateStatus {
  pools?: UpdatePoolStatus[];
}

// Version
export interface Version {
  count: string;
  pods?: string;
  site?: string;
  version: string;
}

// Daemon Status
export interface DaemonStatus {
  clusterID?: string;
  clusterName?: string;
  conditions?: Condition[];
  cordonAndDrain?: CordonAndDrain;
  minimumReleaseLevel?: string;
  pods?: Pods;
  podsStatus?: PodsStatus;
  quorumPods?: QuorumPods;
  roles?: RoleStatus[];
  statusDetails?: StatusDetails;
  tiebreaker?: Tiebreaker;
  update?: UpdateStatus;
  versions?: Version[];
}

// Main Daemon Resource
export interface Daemon {
  apiVersion?: string;
  kind?: string;
  metadata?: ObjectMeta;
  spec?: DaemonSpec;
  status?: DaemonStatus;
}

// Daemon List
export interface DaemonList {
  apiVersion?: string;
  items: Daemon[];
  kind?: string;
  metadata?: ObjectMeta;
}
