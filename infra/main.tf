terraform {
  required_version = ">= 1.5"
  required_providers {
    google = { source = "hashicorp/google", version = "~> 6.0" }
  }
}
variable "project_id" { type = string }
variable "bucket_name" { type = string }
variable "archive_writer_member" {
  type        = string
  description = "Dedicated identity, e.g. serviceAccount:notary-archive@PROJECT.iam.gserviceaccount.com"
}
variable "location" {
  type    = string
  default = "US"
}
provider "google" { project = var.project_id }
resource "google_storage_bucket" "journal" {
  name                        = var.bucket_name
  location                    = var.location
  uniform_bucket_level_access = true
  public_access_prevention    = "enforced"
  force_destroy               = false
  versioning { enabled = true }
  retention_policy {
    retention_period = 316224000 # 3660 days; conservative ten-calendar-year minimum
    is_locked        = false     # Lock only after reviewing the irreversible commitment.
  }
  labels = { purpose = "notary-journal" }
}
resource "google_project_iam_custom_role" "archive" {
  role_id     = "notaryArchiveWriter"
  title       = "Notary archive writer and verifier"
  permissions = ["storage.buckets.get", "storage.objects.create", "storage.objects.get", "storage.objects.list"]
}
resource "google_storage_bucket_iam_member" "archive" {
  bucket = google_storage_bucket.journal.name
  role   = google_project_iam_custom_role.archive.name
  member = var.archive_writer_member
}
output "bucket_name" { value = google_storage_bucket.journal.name }
