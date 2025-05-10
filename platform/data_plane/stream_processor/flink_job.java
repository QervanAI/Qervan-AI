// flink_job.java - Enterprise Real-Time Stream Processing Core
package ai.nuzon.processing;

import org.apache.flink.api.common.eventtime.WatermarkStrategy;
import org.apache.flink.api.common.functions.RichFlatMapFunction;
import org.apache.flink.api.common.serialization.SimpleStringSchema;
import org.apache.flink.api.java.tuple.Tuple3;
import org.apache.flink.api.java.utils.ParameterTool;
import org.apache.flink.connector.base.DeliveryGuarantee;
import org.apache.flink.connector.kafka.sink.KafkaRecordSerializationSchema;
import org.apache.flink.connector.kafka.sink.KafkaSink;
import org.apache.flink.connector.kafka.source.KafkaSource;
import org.apache.flink.connector.kafka.source.enumerator.initializer.OffsetsInitializer;
import org.apache.flink.streaming.api.datastream.DataStream;
import org.apache.flink.streaming.api.environment.StreamExecutionEnvironment;
import org.apache.flink.util.Collector;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Duration;
import java.util.Properties;

public class RealTimeProcessingJob {
    
    private static final Logger LOG = LoggerFactory.getLogger(RealTimeProcessingJob.class);
    
    public static void main(String[] args) throws Exception {
        final ParameterTool params = ParameterTool.fromArgs(args);
        final StreamExecutionEnvironment env = configureEnvironment(params);
        
        // Configure enterprise-grade Kafka source with SSL
        KafkaSource<String> source = KafkaSource.<String>builder()
            .setBootstrapServers(params.getRequired("kafka.brokers"))
            .setTopics(params.get("input.topic", "nuzon-events"))
            .setGroupId("nuzon-flink-consumer")
            .setStartingOffsets(OffsetsInitializer.earliest())
            .setProperties(kafkaSecurityConfig(params))
            .setValueOnlyDeserializer(new SimpleStringSchema())
            .build();

        DataStream<String> stream = env.fromSource(
            source, 
            WatermarkStrategy
                .<String>forBoundedOutOfOrderness(Duration.ofMillis(500))
                .withIdleness(Duration.ofMinutes(5)),
            "Enterprise Kafka Source"
        );

        // Processing pipeline
        stream
            .flatMap(new EventParser())
            .keyBy(event -> event.f0) // Agent ID as key
            .process(new StatefulValidator())
            .name("Core Processing Pipeline")
            .addSink(createEnterpriseSink(params));

        env.execute("Nuzon AI Real-Time Processing");
    }

    private static StreamExecutionEnvironment configureEnvironment(ParameterTool params) {
        StreamExecutionEnvironment env = StreamExecutionEnvironment.getExecutionEnvironment();
        
        // Enterprise runtime configuration
        env.setParallelism(params.getInt("parallelism", 16));
        env.enableCheckpointing(params.getLong("checkpoint.interval", 60000));
        env.getCheckpointConfig().setMinPauseBetweenCheckpoints(30000);
        env.getCheckpointConfig().setTolerableCheckpointFailureNumber(3);
        
        // State backend configuration
        env.setStateBackend(new RocksDBStateBackend(
            params.get("state.backend.path", "hdfs://nns/checkpoints"),
            params.getBoolean("incremental.checkpoints", true)
        ));
        
        return env;
    }

    private static KafkaSink<Tuple3<String, String, Double>> createEnterpriseSink(ParameterTool params) {
        return KafkaSink.<Tuple3<String, String, Double>>builder()
            .setBootstrapServers(params.getRequired("kafka.brokers"))
            .setRecordSerializer(KafkaRecordSerializationSchema.builder()
                .setTopic(params.get("output.topic", "nuzon-results"))
                .setValueSerializationSchema(new JsonSerializationSchema())
                .build())
            .setDeliverGuarantee(DeliveryGuarantee.EXACTLY_ONCE)
            .setKafkaProducerConfig(kafkaSecurityConfig(params))
            .build();
    }

    private static Properties kafkaSecurityConfig(ParameterTool params) {
        Properties props = new Properties();
        if (params.has("security.protocol")) {
            props.put("security.protocol", params.get("security.protocol"));
            props.put("ssl.truststore.location", params.get("ssl.truststore.location"));
            props.put("ssl.truststore.password", params.get("ssl.truststore.password"));
            props.put("ssl.keystore.location", params.get("ssl.keystore.location")); 
            props.put("ssl.keystore.password", params.get("ssl.keystore.password"));
            props.put("ssl.key.password", params.get("ssl.key.password"));
        }
        return props;
    }

    // Enterprise data model
    public static class AgentEvent {
        public String agentId;
        public String eventType;
        public double value;
        public long timestamp;
    }

    // Custom serialization schema for enterprise data format
    public static class JsonSerializationSchema 
        implements KafkaRecordSerializationSchema<Tuple3<String, String, Double>> {
        
        @Override
        public ProducerRecord<byte[], byte[]> serialize(
            Tuple3<String, String, Double> element,
            KafkaSinkContext context,
            Long timestamp) {
            
            String json = String.format(
                "{\"agent\":\"%s\",\"metric\":\"%s\",\"value\":%.2f}",
                element.f0,
                element.f1,
                element.f2
            );
            return new ProducerRecord<>(
                context.getTopic(),
                null,
                System.currentTimeMillis(),
                null,
                json.getBytes(StandardCharsets.UTF_8)
            );
        }
    }

    // Complex event processing logic
    private static class EventParser extends RichFlatMapFunction<String, AgentEvent> {
        
        private transient Counter malformedCounter;
        
        @Override
        public void open(Configuration parameters) {
            malformedCounter = getRuntimeContext()
                .getMetricGroup()
                .counter("malformedEvents");
        }

        @Override
        public void flatMap(String value, Collector<AgentEvent> out) {
            try {
                AgentEvent event = parseEvent(value);
                out.collect(event);
            } catch (Exception e) {
                malformedCounter.inc();
                LOG.warn("Malformed event detected: {}", value);
            }
        }
        
        private AgentEvent parseEvent(String raw) throws JSONException {
            JSONObject json = new JSONObject(raw);
            AgentEvent event = new AgentEvent();
            event.agentId = json.getString("agent_id");
            event.eventType = json.getString("event_type");
            event.value = json.getDouble("value");
            event.timestamp = json.getLong("timestamp");
            return event;
        }
    }

    // Stateful validation with enterprise rules
    private static class StatefulValidator extends KeyedProcessFunction<String, AgentEvent, Tuple3<String, String, Double>> {
        
        private transient ValueState<Double> lastValidValue;
        
        @Override
        public void open(Configuration parameters) {
            ValueStateDescriptor<Double> descriptor = 
                new ValueStateDescriptor<>("lastValid", Double.class);
            lastValidValue = getRuntimeContext().getState(descriptor);
        }

        @Override
        public void processElement(
            AgentEvent event,
            KeyedProcessFunction<String, AgentEvent, Tuple3<String, String, Double>>.Context ctx,
            Collector<Tuple3<String, String, Double>> out) throws Exception {
            
            if (isValidEvent(event)) {
                Double previous = lastValidValue.value();
                if (previous == null || Math.abs(event.value - previous) < 1e6) {
                    lastValidValue.update(event.value);
                    out.collect(Tuple3.of(event.agentId, event.eventType, event.value));
                }
            }
        }
        
        private boolean isValidEvent(AgentEvent event) {
            return event.value >= 0 && 
                   event.timestamp > System.currentTimeMillis() - 86400000 &&
                   !event.eventType.isEmpty();
        }
    }
}
