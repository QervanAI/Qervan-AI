# spark_dag.py - Enterprise Batch Analytics Pipeline
from pyspark.sql import SparkSession
from pyspark.sql.functions import *
from pyspark.sql.types import *
import logging
import os

class EnterpriseBatchAnalytics:
    def __init__(self):
        self.spark = self.configure_spark()
        self.logger = self.configure_logging()
        
    def configure_spark(self):
        return SparkSession.builder \
            .appName("WavineAgentAnalytics") \
            .config("spark.serializer", "org.apache.spark.serializer.KryoSerializer") \
            .config("spark.hadoop.fs.s3a.aws.credentials.provider", "com.amazonaws.auth.WebIdentityTokenCredentialsProvider") \
            .config("spark.sql.parquet.datetimeRebaseModeInWrite", "CORRECTED") \
            .config("spark.sql.shuffle.partitions", 2000) \
            .config("spark.executor.instances", 100) \
            .config("spark.dynamicAllocation.enabled", "true") \
            .config("spark.security.credentials.aws.role", os.getenv("AWS_IAM_ROLE")) \
            .config("spark.kubernetes.file.upload.path", "s3a://nuzon-spark/jars") \
            .enableHiveSupport() \
            .getOrCreate()

    def configure_logging(self):
        log4j = self.spark._jvm.org.apache.log4j
        logger = log4j.LogManager.getLogger("WavineBatchAnalytics")
        return logger

    def run_pipeline(self):
        try:
            raw_df = self.read_input_data()
            processed_df = self.apply_transformations(raw_df)
            self.write_output(processed_df)
        except Exception as e:
            self.logger.error(f"Pipeline failed: {str(e)}")
            raise
        finally:
            self.cleanup_resources()

    def read_input_data(self):
        return self.spark.read \
            .format("parquet") \
            .option("mergeSchema", "true") \
            .option("pathGlobFilter", "*.parquet") \
            .load("s3a://nuzon-data/agent_events/")
            
    def apply_transformations(self, df):
        return df \
            .withColumn("event_date", to_date(from_unixtime(col("timestamp")/1000))) \
            .filter(col("event_type").isin(["SESSION_START", "ACTION", "SESSION_END"])) \
            .groupBy("agent_id", "event_date") \
            .agg(
                count(when(col("event_type") == "SESSION_START", 1)).alias("sessions"),
                sum(when(col("event_type") == "ACTION", col("value"))).alias("total_actions"),
                avg("processing_latency").alias("avg_latency")
            ) \
            .withColumn("anomaly_flag", expr("""
                CASE WHEN total_actions > 1000 AND avg_latency > 500 THEN 1
                     WHEN total_actions < 10 AND avg_latency > 1000 THEN 1
                     ELSE 0 
                END"""))

    def write_output(self, df):
        df.write \
            .format("iceberg") \
            .mode("overwrite") \
            .option("write.spark.accept-any-schema", "true") \
            .option("overwrite-mode", "dynamic") \
            .save("s3a://nuzon-analytics/agent_metrics/")

    def cleanup_resources(self):
        self.spark.catalog.clearCache()
        self.spark.stop()

if __name__ == "__main__":
    pipeline = EnterpriseBatchAnalytics()
    pipeline.logger.info("Starting enterprise batch processing")
    pipeline.run_pipeline()
