/* bb84_emulator.c - Quantum Key Distribution Simulation Core */
#include <stdio.h>
#include <stdlib.h>
#include <time.h>
#include <math.h>
#include <stdbool.h>

#define PHOTON_COUNT 1024
#define MAX_ANGLE 360.0
#define PI 3.14159265358979323846

typedef enum {RECT, DIAG} Basis;
typedef enum {H, V, P, M} Polarization;

typedef struct {
    Basis basis;
    Polarization state;
    double angle; 
} Photon;

typedef struct {
    bool* raw_bits;
    Basis* bases;
    int length;
} QuantumChannel;

// Quantum operations
void init_quantum_components() {
    srand(time(NULL));
    printf("[QKD] Quantum channel initialized with %d photons\n", PHOTON_COUNT);
}

Photon alice_prepare_photon(int bit) {
    Photon p;
    p.basis = rand() % 2 ? RECT : DIAG;
    
    if(p.basis == RECT) {
        p.state = (bit == 0) ? H : V;
        p.angle = (bit == 0) ? 0.0 : 90.0;
    } else {
        p.state = (bit == 0) ? P : M;
        p.angle = (bit == 0) ? 45.0 : 135.0;
    }
    
    return p;
}

Polarization bob_measure_photon(Photon p, Basis chosen_basis) {
    if(p.basis == chosen_basis) {
        return p.state;
    }
    
    // Basis mismatch - random result
    return (rand() % 2 == 0) ? ((chosen_basis == RECT) ? H : P) : 
                               ((chosen_basis == RECT) ? V : M);
}

QuantumChannel simulate_quantum_channel(bool* alice_bits) {
    QuantumChannel qc;
    qc.raw_bits = malloc(PHOTON_COUNT * sizeof(bool));
    qc.bases = malloc(PHOTON_COUNT * sizeof(Basis));
    qc.length = PHOTON_COUNT;

    for(int i = 0; i < PHOTON_COUNT; i++) {
        Basis b = rand() % 2 ? RECT : DIAG;
        qc.bases[i] = b;
        Photon p = alice_prepare_photon(alice_bits[i]);
        qc.raw_bits[i] = (bob_measure_photon(p, b) == H || bob_measure_photon(p, b) == P) ? 0 : 1;
    }
    
    return qc;
}

// Security verification
double calculate_qber(bool* sifted_key, bool* original_bits, int length) {
    int errors = 0;
    for(int i = 0; i < length; i++) {
        if(sifted_key[i] != original_bits[i]) {
            errors++;
        }
    }
    return (double)errors / length;
}

void eavesdrop_channel(QuantumChannel* qc, float eavesdrop_prob) {
    printf("[SECURITY] Eve attempting interception (%.0f%% photons)\n", eavesdrop_prob*100);
    
    for(int i = 0; i < qc->length; i++) {
        if((float)rand()/RAND_MAX < eavesdrop_prob) {
            Basis original_basis = qc->bases[i];
            Basis eve_basis = rand() % 2 ? RECT : DIAG;
            
            Photon p = alice_prepare_photon(qc->raw_bits[i]);
            qc->raw_bits[i] = (bob_measure_photon(p, eve_basis) == H || 
                              bob_measure_photon(p, eve_basis) == P) ? 0 : 1;
            qc->bases[i] = eve_basis;
        }
    }
}

// Key sifting
int sift_key(QuantumChannel* qc, bool* alice_bits, bool* final_key) {
    int key_index = 0;
    for(int i = 0; i < qc->length; i++) {
        if(qc->bases[i] == (alice_bits[i] ? DIAG : RECT)) { // Basis matching
            final_key[key_index++] = qc->raw_bits[i];
        }
    }
    return key_index;
}

int main() {
    init_quantum_components();
    
    bool alice_bits[PHOTON_COUNT];
    for(int i = 0; i < PHOTON_COUNT; i++) {
        alice_bits[i] = rand() % 2;
    }

    QuantumChannel qc = simulate_quantum_channel(alice_bits);
    
    // Uncomment to simulate eavesdropping
    // eavesdrop_channel(&qc, 0.3);
    
    bool final_key[PHOTON_COUNT];
    int key_length = sift_key(&qc, alice_bits, final_key);
    
    double qber = calculate_qber(final_key, alice_bits, key_length);
    printf("[QKD] Generated %d-bit key with QBER %.2f%%\n", 
           key_length, qber*100);
    
    if(qber > 0.12) {
        printf("[SECURITY] Quantum intrusion detected! Discarding key.\n");
    } else {
        printf("[SECURITY] Channel secure. Final key validated.\n");
    }

    free(qc.raw_bits);
    free(qc.bases);
    return 0;
}
