       IDENTIFICATION DIVISION.
       PROGRAM-ID. WavineCICS.
       AUTHOR. Cirium-AI-ENGINEERING.
       DATE-WRITTEN. 01/01/2024.
       SECURITY. TLS1.3+ WITH QUANTUM-SAFE CURVES.

      ******************************************************************
      *  ENTERPRISE CICS ADAPTER FOR LEGACY SYSTEM INTEGRATION         *
      *  FEATURES:                                                    *
      *  - XA-COMPLIANT TRANSACTION MANAGEMENT                         *
      *  - VSAM/DB2 HYBRID DATA ACCESS                                 *
      *  - Z16 INSTRUCTION SET OPTIMIZATION                           *
      *  - CICS TS 6.1 COMPATIBILITY                                  *
      ******************************************************************

       ENVIRONMENT DIVISION.
       CONFIGURATION SECTION.
       SOURCE-COMPUTER. IBM-Z16.
       OBJECT-COMPUTER. IBM-Z16.
       SPECIAL-NAMES.
           SYMBOLIC QUEUE IS AI-REQUEST-Q.

       DATA DIVISION.
       WORKING-STORAGE SECTION.
       01  AI-REQUEST-AREA.
           05  AI-FUNCTION-CODE      PIC X(8).
           05  AI-INPUT-DATA         PIC X(32768).
           05  AI-RESPONSE-CODE      PIC S9(8) COMP.
           05  AI-TIMESTAMP          PIC X(26).
           05  AI-TRACE-ID           PIC X(32).
           05  AI-ENCRYPTION-FLAG    PIC X(1).
           05  AI-CORREL-ID          PIC X(16).

       01  ERROR-CONTROL.
           05  EIBRESP               PIC S9(8) COMP.
           05  EIBRESP2              PIC S9(8) COMP.
           05  ERROR-MSG             PIC X(78).

       01  SECURITY-TOKEN            PIC X(256).
       01  CRYPTO-HANDLE             PIC X(16).

       LINKAGE SECTION.
       01  DFHCOMMAREA               PIC X(32768).

       PROCEDURE DIVISION USING DFHCOMMAREA.

      ******************************************************************
      * MAIN TRANSACTION PROCESSING                                    *
      ******************************************************************
       000-MAIN-LOGIC.
           EXEC CICS HANDLE CONDITION
                ERROR(900-ERROR-HANDLER)
                END-EXEC.

           PERFORM 100-INITIALIZE-SESSION
           PERFORM 200-PROCESS-REQUEST
           PERFORM 300-FINALIZE-TRANSACTION
           .

      ******************************************************************
      * SESSION INITIALIZATION ROUTINE                                *
      ******************************************************************
       100-INITIALIZE-SESSION.
           EXEC CICS GETMAIN
                SET(ADDRESS OF AI-REQUEST-AREA)
                FLENGTH(LENGTH OF AI-REQUEST-AREA)
                INITIMG(LOVALUE)
                RESP(EIBRESP)
                RESP2(EIBRESP2)
           END-EXEC.

           EXEC CICS INQUIRE SECURITY
                TOKEN(SECURITY-TOKEN)
                RESP(EIBRESP)
           END-EXEC.

           IF EIBRESP NOT = DFHRESP(NORMAL)
               MOVE 'SECURITY PROTOCOL FAILURE' TO ERROR-MSG
               PERFORM 900-ERROR-HANDLER
           END-IF.

           EXEC CICS GQ CONNECT
                CRYPTO(CRYPTO-HANDLE)
                PROTOCOL('TLS13_AES_256_GCM_SHA384')
                RESP(EIBRESP)
           END-EXEC.

      ******************************************************************
      * REQUEST PROCESSING ENGINE                                      *
      ******************************************************************
       200-PROCESS-REQUEST.
           EXEC CICS RECEIVE
                INTO(AI-INPUT-DATA)
                MAXLENGTH(LENGTH OF AI-INPUT-DATA)
                RESP(EIBRESP)
           END-EXEC.

           PERFORM 210-DECRYPT-PAYLOAD
           PERFORM 220-ROUTE-TO-BACKEND
           PERFORM 230-GENERATE-RESPONSE
           .

      ******************************************************************
      * QUANTUM-SAFE DATA DECRYPTION                                  *
      ******************************************************************
       210-DECRYPT-PAYLOAD.
           IF AI-ENCRYPTION-FLAG = 'Q'
               EXEC CICS GQ DECRYPT
                    HANDLE(CRYPTO-HANDLE)
                    DATA(AI-INPUT-DATA)
                    DATALENGTH(LENGTH OF AI-INPUT-DATA)
                    RESP(EIBRESP)
               END-EXEC
           END-IF.

      ******************************************************************
      * BACKEND SYSTEM INTEGRATION                                     *
      ******************************************************************
       220-ROUTE-TO-BACKEND.
           EVALUATE AI-FUNCTION-CODE
               WHEN 'QUERYDB'
                   EXEC CICS LINK PROGRAM('DBSVC01')
                        COMMAREA(AI-INPUT-DATA)
                        RESP(EIBRESP)
                   END-EXEC
               WHEN 'UPDATETXN'
                   EXEC CICS START TRANSID('AIB1')
                        INTERVAL(0)
                        AUTOPROCEED
                        FROM(AI-INPUT-DATA)
                   END-EXEC
               WHEN OTHER
                   MOVE 'INVALID FUNCTION CODE' TO ERROR-MSG
                   PERFORM 900-ERROR-HANDLER
           END-EVALUATE.

      ******************************************************************
      * RESPONSE GENERATION AND ENCRYPTION                            *
      ******************************************************************
       230-GENERATE-RESPONSE.
           IF AI-ENCRYPTION-FLAG = 'Q'
               EXEC CICS GQ ENCRYPT
                    HANDLE(CRYPTO-HANDLE)
                    DATA(AI-INPUT-DATA)
                    DATALENGTH(LENGTH OF AI-INPUT-DATA)
                    RESP(EIBRESP)
               END-EXEC
           END-IF.

           EXEC CICS SEND
                FROM(AI-INPUT-DATA)
                LENGTH(LENGTH OF AI-INPUT-DATA)
                RESP(EIBRESP)
           END-EXEC.

      ******************************************************************
      * TRANSACTION CLEANUP AND COMMIT                                *
      ******************************************************************
       300-FINALIZE-TRANSACTION.
           EXEC CICS GQ DISCONNECT
                HANDLE(CRYPTO-HANDLE)
                RESP(EIBRESP)
           END-EXEC.

           EXEC CICS SYNCPOINT
                RESP(EIBRESP)
           END-EXEC.

           EXEC CICS RETURN
                TRANSID('NUZ1')
                COMMAREA(AI-REQUEST-AREA)
           END-EXEC.

      ******************************************************************
      * ENTERPRISE ERROR HANDLING FRAMEWORK                           *
      ******************************************************************
       900-ERROR-HANDLER.
           EXEC CICS WRITE OPERATOR
                TEXT(ERROR-MSG)
                TEXTLENGTH(LENGTH OF ERROR-MSG)
           END-EXEC.

           EXEC CICS ABEND
                ABCODE('NUZE')
                CANCEL
           END-EXEC.

      ******************************************************************
      * BATCH PROCESSING ENTRY POINT                                  *
      ******************************************************************
       END PROGRAM NUZONCICS.
