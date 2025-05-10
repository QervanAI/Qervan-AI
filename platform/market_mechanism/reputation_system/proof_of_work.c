/* proof_of_work.c - Enterprise-Grade Sybil Resistance System */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <openssl/sha.h>
#include <time.h>
#include <pthread.h>
#include <stdatomic.h>

#define MAX_NONCE 0xFFFFFFFF
#define DIFFICULTY_WINDOW 128
#define TARGET_ADJUST_INTERVAL 60 /* seconds */

typedef struct {
    unsigned char challenge[32];
    unsigned char target[32];
    unsigned int difficulty;
    atomic_uint_fast64_t attempts;
    time_t last_adjust;
} PoWContext;

/* Cryptographic challenge generator */
void generate_challenge(PoWContext* ctx) {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    SHA256((unsigned char*)&ts, sizeof(ts), ctx->challenge);
}

/* Dynamic difficulty adjustment */
void adjust_difficulty(PoWContext* ctx) {
    time_t now = time(NULL);
    if (difftime(now, ctx->last_adjust) > TARGET_ADJUST_INTERVAL) {
        double hash_rate = ctx->attempts / TARGET_ADJUST_INTERVAL;
        uint32_t new_diff = (uint32_t)(hash_rate / 1000); /* Target 1s solve time */
        
        new_diff = new_diff < 1 ? 1 : (new_diff > 0xFFFF ? 0xFFFF : new_diff);
        memset(ctx->target, 0xFF, sizeof(ctx->target));
        for(int i=0; i<sizeof(ctx->target); i++) {
            if(new_diff <= 8*(i+1)) {
                ctx->target[i] = 0xFF << (8 - (new_diff % 8));
                break;
            }
            ctx->target[i] = 0x00;
            new_diff -= 8;
        }
        
        ctx->difficulty = new_diff;
        ctx->attempts = 0;
        ctx->last_adjust = now;
    }
}

/* Thread-safe PoW computation */
typedef struct {
    PoWContext* ctx;
    unsigned int start_nonce;
    unsigned int end_nonce;
    unsigned int* found_nonce;
    unsigned char found_hash[SHA256_DIGEST_LENGTH];
} ThreadData;

void* compute_range(void* arg) {
    ThreadData* data = (ThreadData*)arg;
    unsigned char buffer[64];
    unsigned char hash[SHA256_DIGEST_LENGTH];
    
    memcpy(buffer, data->ctx->challenge, 32);
    for(unsigned int nonce = data->start_nonce; 
        nonce <= data->end_nonce && !*(data->found_nonce); 
        nonce++) {
        
        memcpy(buffer+32, &nonce, sizeof(nonce));
        SHA256(buffer, sizeof(buffer), hash);
        
        atomic_fetch_add(&data->ctx->attempts, 1);
        
        /* Check if hash meets target */
        int valid = 1;
        for(int i=0; i<sizeof(data->ctx->target); i++) {
            if((hash[i] & data->ctx->target[i]) != data->ctx->target[i]) {
                valid = 0;
                break;
            }
        }
        
        if(valid) {
            *data->found_nonce = nonce;
            memcpy(data->found_hash, hash, SHA256_DIGEST_LENGTH);
            break;
        }
    }
    return NULL;
}

int proof_of_work(PoWContext* ctx, unsigned int threads) {
    pthread_t workers[threads];
    ThreadData data[threads];
    unsigned int found_nonce = 0;
    unsigned int range = MAX_NONCE / threads;
    
    adjust_difficulty(ctx);
    
    for(unsigned int i=0; i<threads; i++) {
        data[i].ctx = ctx;
        data[i].start_nonce = i * range;
        data[i].end_nonce = (i+1) * range -1;
        data[i].found_nonce = &found_nonce;
        memset(data[i].found_hash, 0, SHA256_DIGEST_LENGTH);
        
        pthread_create(&workers[i], NULL, compute_range, &data[i]);
    }
    
    for(unsigned int i=0; i<threads; i++) {
        pthread_join(workers[i], NULL);
    }
    
    return found_nonce ? 1 : 0;
}

/* Verification function */
int verify_pow(PoWContext* ctx, unsigned int nonce) {
    unsigned char buffer[64];
    unsigned char hash[SHA256_DIGEST_LENGTH];
    
    memcpy(buffer, ctx->challenge, 32);
    memcpy(buffer+32, &nonce, sizeof(nonce));
    SHA256(buffer, sizeof(buffer), hash);
    
    for(int i=0; i<sizeof(ctx->target); i++) {
        if((hash[i] & ctx->target[i]) != ctx->target[i]) {
            return 0;
        }
    }
    return 1;
}

int main() {
    PoWContext ctx;
    generate_challenge(&ctx);
    
    printf("Starting Proof-of-Work computation (Difficulty: %u)\n", ctx.difficulty);
    if(proof_of_work(&ctx, 8)) {
        printf("Valid solution found!\n");
    } else {
        printf("No solution found in range\n");
    }
    
    return 0;
}
