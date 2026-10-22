export type UserRole='admin'|'viewer'
export interface UserAccount {username:string;role:UserRole;must_change_password:boolean;created_at:string}
export interface Center {deployment?:{reselection_required?:boolean;migration?:{completed:boolean};root:string;status:string;modules:ComposeModule[]};id?:string;name:string;host:string;port:number;console_port:number;username:string;auth_type:'password'|'private_key';password?:string;private_key?:string;passphrase?:string;host_key?:string;docker_socket:string;accelerator:'auto'|'nvidia'|'denglin'|'cpu';detected_accelerator?:string;cpu_count:number;gpu_count:number;compose_file:string;initialized_at?:string;description:string;wms_url?:string;mis_auth?:string;mis_sort_enabled?:boolean;test_mode?:boolean;debug_log_enabled?:boolean;updated_at?:string}
export interface Source {id?:string;name:string;repository:string;description:string;updated_at?:string}
export interface GitCredential {base_url:string;username:string;token?:string;configured:boolean;updated_at?:string}
export interface Version {ref:string;commit:string;kind:'branch'|'tag'}
export interface Checkout {source_id:string;ref:string;commit:string;path:string;created_at:string}
export interface Task {id:string;type:string;status:string;progress:number;message:string;error?:string;params:Record<string,string>;log_count:number;created_at:string}
export interface OverviewContainer {id:string;names:string[];image:string;state:string;status:string;source_id?:string;source_name?:string;version?:string;commit?:string}
export interface OverviewVersion {source_id?:string;source_name:string;version:string;commit?:string;container_count:number}
export interface SoftwareImage {id:string;source_id:string;source_name:string;ref:string;commit:string;image:string;created_at:number;size:number;in_use:boolean}
export interface CurrentModelVersion {schema_version:1;model:string;version:string;format_id:string;platform:'onnx'|'nvidia'|'denglin';hardware_profile?:string;manifest_digest:string;target_path:string;artifact_size?:number;pinned:boolean;activated_at:string;healthy:boolean;health_error?:string}
export interface CenterOverview {center:Center;online:boolean;error?:string;accelerator?:string;accelerator_error?:string;containers:OverviewContainer[];versions:OverviewVersion[];models:CurrentModelVersion[];models_error?:string}
export interface OSSCredential {endpoint:string;bucket:string;access_key_id:string;access_key_secret?:string;images_prefix:string;models_prefix:string;configured:boolean;updated_at?:string}
export interface AlertCredential {feishu_webhook?:string;configured:boolean;updated_at?:string}
export interface MonitoringCenterStatus {center_id:string;name:string;site?:string;target?:string;test_mode?:boolean;monitored:boolean;reason?:string;up?:boolean}
export interface MonitoringStatus {stack_online:boolean;error?:string;centers:MonitoringCenterStatus[]}
export interface RegistrySettings {endpoint:string;configured:boolean;updated_at?:string}
export interface RegistryStorage {root_directory:string;exists:boolean;updated_at?:string}
export interface RegistryPlatformTag {platform:string;tag:string;digest:string}
export interface RegistrySoftwareVersion {version:string;ref:string;commit:string;created_at:string;platforms:RegistryPlatformTag[]}
export interface RegistrySourceVersions {repository:string;source_id:string;source_name:string;versions:RegistrySoftwareVersion[]}
export interface BaseImageArchive {key:string;name:string;platform:'common'|'nvidia'|'denglin';size:number;etag:string;last_modified:string}
export interface ModelArtifact {path:string;target_path:string;size:number;sha256:string}
export type ModelPlatform='onnx'|'nvidia'|'denglin'
export interface ModelFormat {id:string;platform:ModelPlatform;hardware_profile?:string;repository_path:string;artifacts:ModelArtifact[]}
export interface ModelVersion {schema_version:1;formats:ModelFormat[];manifest_key:string;manifest_digest:string;model:string;version:string;created_at:string}
export interface EmptyModelFolder {model:string;files:number;size:number}
export interface ModelUploadFile {path:string;size:number;upload_url?:string}
export interface ModelUploadFormat {id:string;platform:ModelPlatform;hardware_profile?:string;files:ModelUploadFile[]}
export interface ModelUpload {upload_id?:string;model:string;version:string;formats:ModelUploadFormat[]}
export interface ComposeDocument {center_id:string;path:string;content:string}
export interface HardwareGPU {model:string;count:number;memory_bytes?:number}
export interface HardwareDisk {device:string;mount_point:string;total_bytes:number;used_bytes:number;available_bytes:number;usage:string}
export interface HardwareInfo {accelerator:string;cpu:{model:string;sockets:number;physical_cores:number;logical_cores:number};gpus:HardwareGPU[];memory:{total_bytes:number};disks:HardwareDisk[];detected_at:string}

export interface ComposeModule {id:string;name:string;project:string;compose_file:string;enabled:boolean;depends_on:string[];init_services:string[];model_services:string[];ready_urls:string[];timeout:number;status:string;runtime_status?:string;error?:string;revision:string;applied_revision:string;updated_at:string}
