output "public_ip" {
  description = "Public IP of the instance; point the DNS A record for NOTARX_DOMAIN at it."
  value       = oci_core_instance.this.public_ip
}

output "bucket_name" {
  description = "Name of the private backup bucket."
  value       = oci_objectstorage_bucket.backups.name
}

output "s3_endpoint" {
  description = "S3-compatible endpoint of the backup bucket's region."
  value       = "https://${data.oci_objectstorage_namespace.this.namespace}.compat.objectstorage.${var.region}.oraclecloud.com"
}

output "s3_access_key" {
  description = "Access key ID of the backup user's customer secret key."
  value       = oci_identity_customer_secret_key.backup.id
}

output "s3_secret_key" {
  description = "Secret of the backup user's customer secret key. Sensitive; also stored in the state."
  value       = oci_identity_customer_secret_key.backup.key
  sensitive   = true
}
